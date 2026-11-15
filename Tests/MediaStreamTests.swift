import AVFoundation
import XCTest
@testable import myFiles

private final class MediaProtocol: URLProtocol {
    static var respond: ((URLRequest) throws -> (Int, [String: String], Data))?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (code, headers, data) = try Self.respond!(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: "HTTP/1.1", headerFields: headers)!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class MediaStreamTests: XCTestCase {
    override func tearDown() { MediaProtocol.respond = nil }

    @MainActor
    func testNativeAssetReadsMetadataPastFourMiBThroughCappedResponses() async throws {
        // Insert a valid free box before moov, leaving sample offsets intact. This
        // puts required movie metadata beyond Quantum's cap without a large fixture.
        let fixture = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "range-probe", withExtension: "mp4"))
        var media = try Data(contentsOf: fixture)
        var cursor = 0
        while cursor + 8 <= media.count {
            let size = media[cursor..<cursor + 4].reduce(0) { ($0 << 8) | Int($1) }
            if String(data: media[cursor + 4..<cursor + 8], encoding: .ascii) == "moov" { break }
            guard size >= 8 else { throw URLError(.cannotDecodeContentData) }
            cursor += size
        }
        XCTAssertLessThan(cursor, media.count)
        let padding = 5 << 20
        var boxSize = UInt32(padding).bigEndian
        var box = withUnsafeBytes(of: &boxSize) { Data($0) }
        box.append(Data("free".utf8)); box.append(Data(repeating: 0, count: padding - 8))
        media.insert(contentsOf: box, at: cursor)
        let content = media
        let lock = NSLock()
        var offsets: [Int] = []
        MediaProtocol.respond = { request in
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-token")
            XCTAssertFalse(request.url!.absoluteString.contains("synthetic-token"))
            if request.url?.path.hasSuffix("/resources") == true {
                return (200, ["Content-Type": "application/json"],
                        Data(#"{"name":"probe.mp4","path":"/probe.mp4","type":"video/mp4","viewToken":"synthetic-grant"}"#.utf8))
            }
            XCTAssertEqual(request.value(forHTTPHeaderField: "Accept-Encoding"), "identity")
            let range = try XCTUnwrap(request.value(forHTTPHeaderField: "Range"))
            let bounds = range.dropFirst(6).split(separator: "-")
            let start = try XCTUnwrap(Int(bounds[0]))
            let requestedEnd = try XCTUnwrap(Int(bounds[1]))
            guard start < content.count else { return (416, [:], Data()) }
            XCTAssertLessThanOrEqual(requestedEnd - start + 1, MediaRange.chunkSize)
            // Also tolerate a server/proxy cap smaller than Quantum's.
            let end = min(requestedEnd, start + 65535, content.count - 1)
            lock.lock(); offsets.append(start); lock.unlock()
            return (206, ["Content-Type": "video/mp4", "Content-Range": "bytes \(start)-\(end)/\(content.count)"],
                    content.subdata(in: start..<end + 1))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MediaProtocol.self]
        let client = QuantumClient(connection: Connection(name: "Fixture", url: URL(string: "https://files.example/quantum")!),
                                   token: "synthetic-token", configuration: config)
        defer { client.close() }
        let request = try await client.streamRequest(source: "disk", path: "/probe.mp4")
        let stream = MediaStream(client: client, request: request, name: "probe.mp4")
        defer { stream.cancel() }
        let asset = stream.asset()
        let loaded = expectation(description: "Native asset loaded through capped byte ranges")
        let operation = Task { @MainActor in
            defer { loaded.fulfill() }
            do {
                let playable = try await asset.load(.isPlayable)
                XCTAssertTrue(playable)
                let duration = try await asset.load(.duration)
                XCTAssertEqual(duration.seconds, 1, accuracy: 0.1)
                let requestedOffsets = lock.withLock { offsets }
                XCTAssertTrue(requestedOffsets.contains { $0 > (4 << 20) }, "Native loader must reach metadata past the response cap")
            } catch {
                if !Task.isCancelled { XCTFail("Native asset loading failed: \(error)") }
            }
        }
        defer { operation.cancel(); asset.cancelLoading() }
        await fulfillment(of: [loaded], timeout: 30)
    }
}
