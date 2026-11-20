import XCTest
@testable import myFiles

final class MediaRangeTests: XCTestCase {
    func testLargeRangeIsFullyDelivered() async throws {
        let total = 5 * (1 << 20) + 37
        var received = Data()
        var offsets: [Int64] = []
        try await MediaRange.read(offset: 0, length: Int64(total), fetch: { offset, count in
            offsets.append(offset)
            XCTAssertLessThanOrEqual(count, 1 << 20)
            return MediaChunk(data: Data(repeating: UInt8(offset % 251), count: count),
                              totalLength: Int64(total), mimeType: "video/mp4")
        }, receive: { received.append($0.data) })
        XCTAssertEqual(offsets, [0, 1048576, 2097152, 3145728, 4194304, 5242880])
        XCTAssertEqual(received.count, total)
        for offset in offsets { XCTAssertEqual(received[Int(offset)], UInt8(offset % 251)) }
    }

    func testSeekDeliversSmallFirstChunkThenUsesThroughputChunks() async throws {
        var lengths: [Int] = []
        try await MediaRange.read(offset: 3_000_000, length: 2_000_000, firstChunkSize: 65536, fetch: { offset, count in
            lengths.append(count)
            return MediaChunk(data: Data(repeating: 1, count: count), totalLength: 10_000_000, mimeType: "video/mp4")
        }, receive: { _ in })
        XCTAssertEqual(lengths.first, 65536)
        XCTAssertEqual(lengths.dropFirst().first, MediaRange.chunkSize)
        XCTAssertEqual(lengths.reduce(0, +), 2_000_000)
    }

    func testShortServerChunksAndSeekingToEnd() async throws {
        var offsets: [Int64] = []
        var received = Data()
        try await MediaRange.read(offset: 9000, length: nil, fetch: { offset, count in
            offsets.append(offset)
            return MediaChunk(data: Data(repeating: 7, count: min(count, 300, Int(10000 - offset))),
                              totalLength: 10000, mimeType: nil)
        }, receive: { received.append($0.data) })
        XCTAssertEqual(offsets, [9000, 9300, 9600, 9900])
        XCTAssertEqual(received.count, 1000)
    }

    func testCancellationDoesNotDeliverOrFetchMoreData() async throws {
        let task = Task {
            try await MediaRange.read(offset: 0, length: nil, fetch: { _, _ in
                withUnsafeCurrentTask { $0?.cancel() }
                return MediaChunk(data: Data([1]), totalLength: 500, mimeType: nil)
            }, receive: { _ in XCTFail("Cancelled data must not reach the player") })
        }
        do { try await task.value; XCTFail("Expected cancellation") }
        catch is CancellationError { }
    }

    func testChangingLengthAndEmptyChunksAreRejected() async throws {
        for empty in [false, true] {
            var calls = 0
            do {
                try await MediaRange.read(offset: 0, length: nil, fetch: { _, _ in
                    calls += 1
                    return MediaChunk(data: empty ? Data() : Data([1]), totalLength: Int64(10 + calls), mimeType: nil)
                }, receive: { _ in })
                XCTFail("Invalid stream accepted")
            } catch let error as URLError { XCTAssertEqual(error.code, .badServerResponse) }
            XCTAssertEqual(calls, empty ? 1 : 2)
        }
    }

    func testContentRangeValidationRejectsMismatchTruncationAndFullResponses() throws {
        let url = URL(string: "https://files.example/stream")!
        for (code, range, count) in [(206, "bytes 1-2/10", 2), (206, "bytes 0-2/10", 2),
                                     (206, "bytes 0-1/*", 2), (206, "bytes 0-1/1", 2),
                                     (200, "bytes 0-1/10", 2), (206, "bytes 0-3/10", 4)] {
            let response = HTTPURLResponse(url: url, statusCode: code, httpVersion: nil,
                                           headerFields: ["Content-Range": range])!
            XCTAssertThrowsError(try MediaChunk.decode(Data(repeating: 0, count: count), response: response, offset: 0, length: 2))
        }
        let response = HTTPURLResponse(url: url, statusCode: 206, httpVersion: nil,
                                       headerFields: ["Content-Range": "bytes 9000-9999/10000", "Content-Type": "video/mp4"])!
        let chunk = try MediaChunk.decode(Data(repeating: 0, count: 1000), response: response, offset: 9000, length: 2000)
        XCTAssertEqual(chunk.totalLength, 10000)
        XCTAssertEqual(chunk.mimeType, "video/mp4")
    }
}
