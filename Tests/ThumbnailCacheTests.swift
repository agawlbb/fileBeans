import XCTest
import UIKit
@testable import myFiles

private actor ThumbnailFetchCounter {
    private(set) var count = 0
    func fetch(_ data: Data) async throws -> Data {
        count += 1
        try await Task.sleep(for: .milliseconds(30))
        return data
    }
}

private actor ThumbnailFetchGate {
    private var entered = false
    private var ready: CheckedContinuation<Void, Never>?
    private var release: CheckedContinuation<Void, Never>?
    func fetch(_ data: Data) async -> Data {
        entered = true; ready?.resume(); ready = nil
        await withCheckedContinuation { release = $0 }
        return data
    }
    func waitUntilEntered() async {
        if entered { return }
        await withCheckedContinuation { ready = $0 }
    }
    func finish() { release?.resume(); release = nil }
}

@MainActor
final class ThumbnailCacheTests: XCTestCase {
    private func makeKey(connection: UUID = UUID(), revision: String = "1") -> ThumbnailKey {
        ThumbnailKey(connection: connection, provider: "filesystem", source: "VPS", file: "file", revision: revision)
    }
    private func temporaryRoot() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    }
    private func png() -> Data {
        UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2)).pngData { context in
            UIColor.red.setFill(); context.fill(CGRect(x: 0, y: 0, width: 2, height: 2))
        }
    }
    private func file(_ root: URL, _ key: ThumbnailKey) -> URL {
        root.appendingPathComponent(key.connection.uuidString).appendingPathComponent("thumbnails").appendingPathComponent(key.digest)
    }
    func testDiskHitAcrossCacheInstancesAndVersionInvalidation() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let key = makeKey(), data = png()
        _ = try await ThumbnailCache(root: root).data(for: key) { data }
        let cache = ThumbnailCache(root: root)
        let hit = try await cache.data(for: key) { XCTFail("Disk hit should not fetch"); return data }
        XCTAssertEqual(hit, data)
        let changed = self.makeKey(connection: key.connection, revision: "2")
        let counter = ThumbnailFetchCounter()
        _ = try await cache.data(for: changed) { try await counter.fetch(data) }
        let count = await counter.count
        XCTAssertEqual(count, 1)
    }
    func testMissingRevisionExpiresAndCorruptDataRefetches() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let key = makeKey(revision: ""), data = png()
        let cache = ThumbnailCache(root: root, lifetime: 0)
        let counter = ThumbnailFetchCounter()
        for _ in 0..<2 { _ = try await cache.data(for: key) { try await counter.fetch(data) } }
        let count = await counter.count
        XCTAssertEqual(count, 2)
        let versioned = self.makeKey(connection: key.connection)
        _ = try await cache.data(for: versioned) { data }
        await cache.clearMemory()
        try Data("broken".utf8).write(to: file(root, versioned))
        _ = try await cache.data(for: versioned) { try await counter.fetch(data) }
        let updated = await counter.count
        XCTAssertEqual(updated, 3)
    }
    func testConcurrentRequestsShareFetchAndConnectionRemovalIsIsolated() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let cache = ThumbnailCache(root: root), key = makeKey(), data = png()
        let counter = ThumbnailFetchCounter()
        async let first = cache.data(for: key) { try await counter.fetch(data) }
        async let second = cache.data(for: key) { try await counter.fetch(data) }
        let results = try await (first, second)
        XCTAssertEqual(results.0, results.1)
        let count = await counter.count
        XCTAssertEqual(count, 1)
        let other = self.makeKey()
        _ = try await cache.data(for: other) { data }
        await cache.clear(connection: key.connection)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file(root, key).path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: file(root, other).path))
        _ = try await cache.data(for: key) { try await counter.fetch(data) }
        let updated = await counter.count
        XCTAssertEqual(updated, 2)
    }
    func testEvictionUsesThumbnailRecencyAndPreservesOtherFiles() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let data = png(), first = makeKey(), second = makeKey()
        let cache = ThumbnailCache(root: root, limit: data.count)
        _ = try await cache.data(for: first) { data }
        let upload = root.appendingPathComponent(first.connection.uuidString).appendingPathComponent("upload.data")
        try Data("pending upload".utf8).write(to: upload)
        try FileManager.default.setAttributes([.modificationDate: Date(timeIntervalSince1970: 0)], ofItemAtPath: file(root, first).path)
        _ = try await cache.data(for: second) { data }
        XCTAssertFalse(FileManager.default.fileExists(atPath: file(root, first).path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: file(root, second).path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: upload.path))
    }
    func testFailedDiskWriteStillReturnsFetchedThumbnail() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        try Data().write(to: root) // A file prevents creating the cache directories.
        let data = png()
        let result = try await ThumbnailCache(root: root).data(for: makeKey()) { data }
        XCTAssertEqual(result, data)
    }
    func testClearRejectsInflightDataEvenWhenFetchIgnoresCancellation() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let cache = ThumbnailCache(root: root), key = makeKey(), data = png()
        let gate = ThumbnailFetchGate()
        let request = Task { try await cache.data(for: key) { await gate.fetch(data) } }
        await gate.waitUntilEntered()
        await cache.clear(connection: key.connection)
        await gate.finish()
        do {
            _ = try await request.value
            XCTFail("Removed connection must reject an old fetch")
        } catch is CancellationError { }
        XCTAssertFalse(FileManager.default.fileExists(atPath: file(root, key).path))
    }
    func testInvalidThumbnailResponseIsNotPersisted() async throws {
        let root = temporaryRoot(); defer { try? FileManager.default.removeItem(at: root) }
        let cache = ThumbnailCache(root: root), key = makeKey()
        do {
            _ = try await cache.data(for: key) { Data("not an image".utf8) }
            XCTFail("Invalid image data should be rejected")
        } catch { }
        XCTAssertFalse(FileManager.default.fileExists(atPath: file(root, key).path))
    }
    func testKeysSeparateProviderSourceConnectionAndVersion() {
        let original = makeKey()
        let alternatives = [makeKey(), makeKey(connection: original.connection, revision: "2"),
            ThumbnailKey(connection: original.connection, provider: "drive", source: "VPS", file: "file", revision: "1"),
            ThumbnailKey(connection: original.connection, provider: "filesystem", source: "Samba", file: "file", revision: "1")]
        XCTAssertTrue(alternatives.allSatisfy { $0.digest != original.digest })
        XCTAssertEqual(original.digest.count, 64)
    }
}
