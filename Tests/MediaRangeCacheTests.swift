import XCTest
@testable import myFiles

@MainActor
final class MediaRangeCacheTests: XCTestCase {
    func testRevisitingAndOverlappingRangesFetchOnlyMissingBytes() async throws {
        let cache = MediaRangeCache()
        var requests: [String] = []
        let fetch: MediaRangeCache.Fetch = { offset, count in
            requests.append("\(offset):\(count)")
            return AsyncThrowingStream { continuation in
                continuation.yield(MediaChunk(data: Data(repeating: UInt8(offset % 251), count: count), totalLength: 10000, mimeType: "video/mp4"))
                continuation.finish()
            }
        }
        var bytes = 0
        try await cache.read(offset: 1000, length: 2000, fetch: fetch) { bytes += $0.data.count }
        try await cache.read(offset: 2000, length: 2000, fetch: fetch) { bytes += $0.data.count }
        let count = requests.count
        try await cache.read(offset: 1000, length: 3000, fetch: fetch) { bytes += $0.data.count }
        XCTAssertEqual(bytes, 7000)
        XCTAssertEqual(requests, ["1000:2000", "3000:1000"])
        XCTAssertEqual(requests.count, count, "Returning to a cached seek range must issue zero requests")
    }

    func testConcurrentReadersShareProgressAndCancellationIsIsolated() async throws {
        let cache = MediaRangeCache()
        let started = expectation(description: "Range request started")
        let firstBytes = expectation(description: "Progress delivered before completion")
        var continuation: AsyncThrowingStream<MediaChunk, Error>.Continuation!
        var requests = 0
        let fetch: MediaRangeCache.Fetch = { _, _ in
            requests += 1
            return AsyncThrowingStream { continuation = $0; started.fulfill() }
        }
        let first = Task { try await cache.read(offset: 0, length: 1000, fetch: fetch) { _ in firstBytes.fulfill() } }
        await fulfillment(of: [started], timeout: 2)
        continuation.yield(MediaChunk(data: Data(repeating: 1, count: 500), totalLength: 2000, mimeType: nil))
        await fulfillment(of: [firstBytes], timeout: 2)
        let secondStarted = expectation(description: "Overlapping reader receives existing progress")
        var secondBytes = Data()
        let second = Task {
            try await cache.read(offset: 250, length: 750, fetch: fetch) { chunk in
                if secondBytes.isEmpty { secondStarted.fulfill() }
                secondBytes.append(chunk.data)
            }
        }
        await fulfillment(of: [secondStarted], timeout: 2)
        first.cancel()
        do { try await first.value; XCTFail("Cancelled consumer must stop") } catch is CancellationError { }
        continuation.yield(MediaChunk(data: Data(repeating: 2, count: 500), totalLength: 2000, mimeType: nil))
        continuation.finish()
        try await second.value
        XCTAssertEqual(requests, 1)
        XCTAssertEqual(secondBytes, Data(repeating: 1, count: 250) + Data(repeating: 2, count: 500))
    }

    func testCancelPreservesCompletedRangesAndClearReleasesThem() async throws {
        let cache = MediaRangeCache(limit: 1000)
        var requests = 0
        let fetch: MediaRangeCache.Fetch = { _, count in
            requests += 1
            return AsyncThrowingStream { continuation in
                continuation.yield(MediaChunk(data: Data(repeating: 3, count: count), totalLength: 10000, mimeType: nil)); continuation.finish()
            }
        }
        try await cache.read(offset: 0, length: 500, fetch: fetch) { _ in }
        cache.cancel()
        try await cache.read(offset: 0, length: 500, fetch: fetch) { _ in }
        XCTAssertEqual(requests, 1, "Paging away must retain completed video ranges")
        cache.clear()
        XCTAssertEqual(cache.byteCount, 0)
        try await cache.read(offset: 0, length: 500, fetch: fetch) { _ in }
        XCTAssertEqual(requests, 2)
    }

    func testEvictionUsesRecencyAndRespectsByteLimit() async throws {
        let cache = MediaRangeCache(limit: 1000)
        var offsets: [Int64] = []
        let fetch: MediaRangeCache.Fetch = { offset, count in
            offsets.append(offset)
            return AsyncThrowingStream { continuation in
                continuation.yield(MediaChunk(data: Data(repeating: 0, count: count), totalLength: 10000, mimeType: nil)); continuation.finish()
            }
        }
        for offset in [Int64(0), 500, 0, 1000, 0, 500] {
            try await cache.read(offset: offset, length: 500, fetch: fetch) { _ in }
            XCTAssertLessThanOrEqual(cache.byteCount, 1000)
        }
        XCTAssertEqual(offsets, [0, 500, 1000, 500])
    }

    func testFailedPartialResponseIsNotCached() async throws {
        let cache = MediaRangeCache()
        let fetch: MediaRangeCache.Fetch = { _, _ in
            AsyncThrowingStream { continuation in
                continuation.yield(MediaChunk(data: Data(repeating: 1, count: 100), totalLength: 1000, mimeType: nil))
                continuation.finish(throwing: URLError(.networkConnectionLost))
            }
        }
        do { try await cache.read(offset: 0, length: 500, fetch: fetch) { _ in }; XCTFail("Partial failure must fail the read") }
        catch { XCTAssertEqual((error as? URLError)?.code, .networkConnectionLost) }
        XCTAssertEqual(cache.byteCount, 0)
    }
}
