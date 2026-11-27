import Foundation
import CryptoKit
import UIKit
import ImageIO

struct ThumbnailKey: Hashable {
    let connection: UUID
    let provider: String
    let source: String
    let file: String
    let revision: String
    var digest: String {
        let fields = [connection.uuidString, provider, source, file, revision]
        let data = try! JSONEncoder().encode(fields)
        return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }
}

private final class ThumbnailMemoryEntry {
    let data: Data
    let created: Date
    init(_ data: Data, created: Date) { self.data = data; self.created = created }
}

/// NSCache is thread safe, so a SwiftUI cell can display a hit without an actor hop.
private final class ThumbnailImages: @unchecked Sendable {
    final class Entry {
        let image: UIImage
        let created = Date()
        init(_ image: UIImage) { self.image = image }
    }
    private let cache = NSCache<NSString, Entry>()
    private let lifetime: TimeInterval
    init(limit: Int, lifetime: TimeInterval) {
        self.lifetime = lifetime
        cache.totalCostLimit = limit
    }
    func image(for key: ThumbnailKey) -> UIImage? {
        guard let entry = cache.object(forKey: key.digest as NSString),
              !key.revision.isEmpty || Date().timeIntervalSince(entry.created) < lifetime else { return nil }
        return entry.image
    }
    func insert(_ image: UIImage, for key: ThumbnailKey) {
        guard let cg = image.cgImage else { return }
        cache.setObject(Entry(image), forKey: key.digest as NSString, cost: cg.bytesPerRow * cg.height)
    }
    func clear() { cache.removeAllObjects() }
}

/// Disk work is serialized off the main actor. Only thumbnail subdirectories are evicted.
actor ThumbnailCache {
    static let shared = ThumbnailCache()
    private let root: URL?
    private let limit: Int
    private let lifetime: TimeInterval
    private var lastPrune = Date.distantPast
    private var diskBytesSincePrune = 0
    private let memory = NSCache<NSString, ThumbnailMemoryEntry>()
    private nonisolated let images: ThumbnailImages
    private var imageTasks: [ThumbnailKey: Task<UIImage, Error>] = [:]
    private var memoryGeneration = 0
    private final class Flight {
        let id = UUID()
        let task: Task<Data, Error>
        var consumers = Set<UUID>()
        init(task: Task<Data, Error>) { self.task = task }
    }
    private var pending: [ThumbnailKey: Flight] = [:]
    private var generations: [UUID: Int] = [:]
    private var activeFetches = 0
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private func fetchSlot() async {
        if activeFetches < 4 { activeFetches += 1; return }
        await withCheckedContinuation { waiters.append($0) }
    }
    private func releaseSlot() {
        if waiters.isEmpty { activeFetches -= 1 }
        else { waiters.removeFirst().resume() }
    }

    init(root: URL? = nil, limit: Int = 200 * 1024 * 1024, lifetime: TimeInterval = 86400,
         imageLimit: Int = 128 * 1024 * 1024) {
        self.root = root; self.limit = limit; self.lifetime = lifetime
        images = ThumbnailImages(limit: imageLimit, lifetime: lifetime)
        memory.countLimit = 200; memory.totalCostLimit = 32 * 1024 * 1024
    }
    private func directory(_ connection: UUID) throws -> URL {
        if let root { return root.appendingPathComponent(connection.uuidString).appendingPathComponent("thumbnails") }
        return try SharedStore().cacheDirectory(connection).appendingPathComponent("thumbnails")
    }
    private func valid(_ created: Date, key: ThumbnailKey) -> Bool {
        !key.revision.isEmpty || Date().timeIntervalSince(created) < lifetime
    }
    func data(for key: ThumbnailKey, fetch: @escaping () async throws -> Data) async throws -> Data {
        let name = key.digest as NSString
        if let entry = memory.object(forKey: name), valid(entry.created, key: key) {
            if let directory = try? directory(key.connection) {
                try? FileManager.default.setAttributes([.modificationDate: Date()], ofItemAtPath: directory.appendingPathComponent(key.digest).path)
            }
            return entry.data
        }
        let url = try directory(key.connection).appendingPathComponent(key.digest)
        if let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
           let created = attributes[.creationDate] as? Date, valid(created, key: key),
           let data = try? Data(contentsOf: url), UIImage(data: data) != nil {
            memory.setObject(ThumbnailMemoryEntry(data, created: created), forKey: name, cost: data.count)
            try? FileManager.default.setAttributes([.modificationDate: Date()], ofItemAtPath: url.path)
            return data
        }
        try? FileManager.default.removeItem(at: url)
        let generation = generations[key.connection, default: 0]
        let flight: Flight
        if let existing = pending[key] { flight = existing }
        else {
            let task = Task {
                await self.fetchSlot()
                defer { self.releaseSlot() }
                try Task.checkCancellation()
                let data = try await fetch()
                try Task.checkCancellation()
                guard self.generations[key.connection, default: 0] == generation else { throw CancellationError() }
                guard UIImage(data: data) != nil else { throw CocoaError(.fileReadCorruptFile) }
                self.memory.setObject(ThumbnailMemoryEntry(data, created: Date()), forKey: name, cost: data.count)
                do {
                    try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
                    try data.write(to: url, options: .atomic)
                    self.diskBytesSincePrune += data.count
                    if Date().timeIntervalSince(self.lastPrune) > 60 || self.diskBytesSincePrune >= max(1, min(self.limit, 4 * 1024 * 1024)) {
                        self.lastPrune = Date(); self.diskBytesSincePrune = 0
                        try self.prune()
                    }
                } catch { }
                return data
            }
            flight = Flight(task: task); pending[key] = flight
        }
        let consumer = UUID()
        flight.consumers.insert(consumer)
        return try await withTaskCancellationHandler {
            defer { release(key, flight: flight.id, consumer: consumer) }
            let data = try await flight.task.value
            try Task.checkCancellation()
            guard generations[key.connection, default: 0] == generation else { throw CancellationError() }
            return data
        } onCancel: {
            Task { await self.release(key, flight: flight.id, consumer: consumer) }
        }
    }
    private func release(_ key: ThumbnailKey, flight id: UUID, consumer: UUID) {
        guard let flight = pending[key], flight.id == id else { return }
        flight.consumers.remove(consumer)
        if flight.consumers.isEmpty { flight.task.cancel(); pending[key] = nil }
    }
    nonisolated func cachedImage(for key: ThumbnailKey) -> UIImage? { images.image(for: key) }

    private nonisolated static func decode(_ data: Data) throws -> UIImage {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: 360,
                kCGImageSourceShouldCacheImmediately: true
              ] as CFDictionary) else { throw CocoaError(.fileReadCorruptFile) }
        return UIImage(cgImage: image)
    }

    func image(for key: ThumbnailKey, fetch: @escaping () async throws -> Data) async throws -> UIImage {
        try Task.checkCancellation()
        if let image = cachedImage(for: key) { return image }
        let generation = generations[key.connection, default: 0]
        let memoryGeneration = self.memoryGeneration
        let task: Task<UIImage, Error>
        if let existing = imageTasks[key] { task = existing }
        else {
            task = Task {
                let data = try await self.data(for: key, fetch: fetch)
                let image = try await Task.detached(priority: .userInitiated) { try Self.decode(data) }.value
                try Task.checkCancellation()
                guard self.generations[key.connection, default: 0] == generation else { throw CancellationError() }
                if self.memoryGeneration == memoryGeneration { self.images.insert(image, for: key) }
                return image
            }
            imageTasks[key] = task
            // Own cleanup independently of any cell that happens to await this task.
            Task {
                _ = await task.result
                if self.generations[key.connection, default: 0] == generation { self.imageTasks[key] = nil }
            }
        }
        let image = try await task.value
        try Task.checkCancellation()
        guard generations[key.connection, default: 0] == generation else { throw CancellationError() }
        return image
    }
    func clear(connection: UUID) {
        generations[connection, default: 0] += 1
        for key in Array(pending.keys) where key.connection == connection { pending.removeValue(forKey: key)?.task.cancel() }
        for key in Array(imageTasks.keys) where key.connection == connection { imageTasks.removeValue(forKey: key)?.cancel() }
        memory.removeAllObjects()
        images.clear()
        if let directory = try? directory(connection) { try? FileManager.default.removeItem(at: directory) }
    }
    func clearMemory() { memoryGeneration += 1; memory.removeAllObjects(); images.clear() }
    private func prune() throws {
        let base: URL
        if let root { base = root }
        else { base = try directory(UUID()).deletingLastPathComponent().deletingLastPathComponent() }
        let manager = FileManager.default
        let connections = try manager.contentsOfDirectory(at: base, includingPropertiesForKeys: nil)
        var files: [(URL, Int, Date)] = []
        for connection in connections {
            let directory = connection.appendingPathComponent("thumbnails")
            for url in (try? manager.contentsOfDirectory(at: directory, includingPropertiesForKeys: [.fileSizeKey, .contentModificationDateKey])) ?? [] {
                let values = try url.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey])
                files.append((url, values.fileSize ?? 0, values.contentModificationDate ?? .distantPast))
            }
        }
        var total = files.reduce(0) { $0 + $1.1 }
        for (url, size, _) in files.sorted(by: { $0.2 < $1.2 }) where total > limit {
            try manager.removeItem(at: url); total -= size
        }
    }
}
