import Foundation
import CryptoKit
import UIKit

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

/// Disk work is serialized off the main actor. Only thumbnail subdirectories are evicted.
actor ThumbnailCache {
    static let shared = ThumbnailCache()
    private let root: URL?
    private let limit: Int
    private let lifetime: TimeInterval
    private let memory = NSCache<NSString, ThumbnailMemoryEntry>()
    private var pending: [ThumbnailKey: Task<Data, Error>] = [:]
    private var generations: [UUID: Int] = [:]

    init(root: URL? = nil, limit: Int = 200 * 1024 * 1024, lifetime: TimeInterval = 86400) {
        self.root = root; self.limit = limit; self.lifetime = lifetime
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
        if let task = pending[key] {
            let data = try await task.value
            guard generations[key.connection, default: 0] == generation else { throw CancellationError() }
            return data
        }
        let task = Task {
            let data = try await fetch()
            guard UIImage(data: data) != nil else { throw CocoaError(.fileReadCorruptFile) }
            return data
        }
        pending[key] = task
        defer { if generations[key.connection, default: 0] == generation { pending[key] = nil } }
        let data = try await task.value
        guard generations[key.connection, default: 0] == generation else { throw CancellationError() }
        memory.setObject(ThumbnailMemoryEntry(data, created: Date()), forKey: name, cost: data.count)
        // A disk failure must never prevent displaying a successfully fetched thumbnail.
        do {
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            try data.write(to: url, options: .atomic)
            try prune()
        } catch { }
        return data
    }
    func clear(connection: UUID) {
        generations[connection, default: 0] += 1
        for key in Array(pending.keys) where key.connection == connection { pending.removeValue(forKey: key)?.cancel() }
        memory.removeAllObjects()
        if let directory = try? directory(connection) { try? FileManager.default.removeItem(at: directory) }
    }
    func clearMemory() { memory.removeAllObjects() }
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
