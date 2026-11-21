import AVFoundation
import XCTest
@testable import myFiles

private final class MediaProtocol: URLProtocol {
    static var respond: ((URLRequest) throws -> (Int, [String: String], Data))?
    static var delay: TimeInterval = 0
    private var delivery: DispatchWorkItem?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (code, headers, data) = try Self.respond!(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: "HTTP/1.1", headerFields: headers)!
            let delivery = DispatchWorkItem { [weak self] in
                guard let self else { return }
                self.client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: data)
                self.client?.urlProtocolDidFinishLoading(self)
            }
            self.delivery = delivery
            DispatchQueue.global().asyncAfter(deadline: .now() + Self.delay, execute: delivery)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() { delivery?.cancel() }
}

final class MediaStreamTests: XCTestCase {
    override func tearDown() { MediaProtocol.respond = nil; MediaProtocol.delay = 0 }

    @MainActor
    func testPrefetchIsBoundedAndPlaybackReusesOpeningBytes() async throws {
        let content = Data(repeating: 0x42, count: 512 * 1024)
        var ranges: [String] = []
        MediaProtocol.respond = { request in
            let range = try XCTUnwrap(request.value(forHTTPHeaderField: "Range"))
            ranges.append(range)
            let bounds = range.dropFirst(6).split(separator: "-")
            let start = try XCTUnwrap(Int(bounds[0]))
            let end = min(try XCTUnwrap(Int(bounds[1])), content.count - 1)
            return (206, ["Content-Type": "video/mp4", "Content-Range": "bytes \(start)-\(end)/\(content.count)"], content.subdata(in: start..<end + 1))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MediaProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Fixture", url: URL(string: "https://files.example")!),
                                      token: "synthetic-token", configuration: config)
        defer { client.close() }
        let request = try client.request("content", query: ["path": "/probe.mp4", "source": "disk"])
        let stream = MediaStream(client: client, request: request, name: "probe.mp4")
        defer { stream.cancel(); stream.clearPrefetch() }
        try await stream.prefetch()
        try await stream.prefetch()
        XCTAssertEqual(stream.loadingRequestCount, 0, "Prefetch must not create a player or begin playback")
        XCTAssertEqual(ranges, ["bytes=0-262143"])
        let probe = try await stream.readChunk(offset: 0, length: 2)
        XCTAssertEqual(probe.data, content.prefix(2))
        XCTAssertEqual(probe.totalLength, Int64(content.count))
        let start = try await stream.readChunk(offset: 2, length: 65536)
        XCTAssertEqual(start.data.count, 65536)
        XCTAssertEqual(ranges.count, 1, "Player probes must reuse the prefetched opening bytes")
        _ = try await stream.readChunk(offset: 262144, length: 65536)
        XCTAssertEqual(ranges.count, 2, "Playback beyond the head must continue using bounded requests")
        stream.clearPrefetch()
        _ = try await stream.readChunk(offset: 0, length: 2)
        XCTAssertEqual(ranges.count, 3, "Eviction must release the prefetched bytes")
    }

    @MainActor
    func testPlaybackAdvancesAndDecodesMultipleFrames() async throws {
        try await assertPlayback(fixtureName: "playback-probe")
    }

    @MainActor
    func testNormalizedHEVCPlaybackAdvancesAndSeeks() async throws {
        try await assertPlayback(fixtureName: "normalization-probe")
    }

    @MainActor
    private func assertPlayback(fixtureName: String) async throws {
        let fixture = try XCTUnwrap(Bundle(for: Self.self).url(forResource: fixtureName, withExtension: "mp4"))
        let content = try Data(contentsOf: fixture)
        MediaProtocol.delay = 0.15
        MediaProtocol.respond = { request in
            let range = try XCTUnwrap(request.value(forHTTPHeaderField: "Range"))
            let bounds = range.dropFirst(6).split(separator: "-")
            let start = try XCTUnwrap(Int(bounds[0]))
            let requestedEnd = try XCTUnwrap(Int(bounds[1]))
            guard start < content.count else { return (416, [:], Data()) }
            let end = min(requestedEnd, start + 32767, content.count - 1)
            return (206, ["Content-Type": "video/mp4", "Content-Range": "bytes \(start)-\(end)/\(content.count)"],
                    content.subdata(in: start..<end + 1))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MediaProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Fixture", url: URL(string: "https://files.example")!),
                                   token: "synthetic-token", configuration: config)
        defer { client.close() }
        let request = try client.request("content", query: ["path": "/probe.mp4", "source": "disk"])
        let stream = MediaStream(client: client, request: request, name: "probe.mp4")
        let player = AVPlayer()
        defer { player.pause(); player.replaceCurrentItem(with: nil); stream.cancel() }
        let prepared = expectation(description: "Player asset preparation completes")
        let preparation = Task { @MainActor in
            defer { prepared.fulfill() }
            do { try await stream.play(on: player) }
            catch { if !Task.isCancelled { XCTFail("Player preparation failed: \(error)") } }
        }
        defer { preparation.cancel() }
        await fulfillment(of: [prepared], timeout: 15)
        guard player.currentItem != nil else { return }
        let output = AVPlayerItemVideoOutput(pixelBufferAttributes: nil)
        player.currentItem?.add(output)
        let deadline = Date().addingTimeInterval(15)
        var decodedFrames = 0
        while Date() < deadline {
            let time = player.currentTime()
            if output.hasNewPixelBuffer(forItemTime: time),
               output.copyPixelBuffer(forItemTime: time, itemTimeForDisplay: nil) != nil { decodedFrames += 1 }
            if time.seconds >= 1.5, decodedFrames >= 3 { break }
            if player.currentItem?.status == .failed { break }
            try await Task.sleep(for: .milliseconds(100))
        }
        let state = "time=\(player.currentTime().seconds), rate=\(player.rate), control=\(player.timeControlStatus.rawValue), waiting=\(player.reasonForWaitingToPlay?.rawValue ?? "none"), item=\(player.currentItem?.status.rawValue ?? -1), requests=\(stream.loadingRequestCount), completed=\(stream.completedRequestCount), bytes=\(stream.suppliedByteCount)"
        XCTAssertGreaterThanOrEqual(player.currentTime().seconds, 1.5, state)
        XCTAssertGreaterThanOrEqual(decodedFrames, 3, "Only \(decodedFrames) video frames decoded; \(state)")
        guard player.currentTime().seconds >= 1.5, decodedFrames >= 3 else { return }

        let sought = expectation(description: "Seek completes")
        player.pause()
        player.seek(to: CMTime(seconds: 5, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero) { finished in
            XCTAssertTrue(finished)
            sought.fulfill()
        }
        await fulfillment(of: [sought], timeout: 10)
        player.play()
        let resumeDeadline = Date().addingTimeInterval(10)
        while player.currentTime().seconds < 6, Date() < resumeDeadline {
            try await Task.sleep(for: .milliseconds(100))
        }
        XCTAssertGreaterThanOrEqual(player.currentTime().seconds, 6, "Playback must advance after seeking and resuming")
    }

    @MainActor
    func testNativeAssetReadsMetadataPastFourMiBThroughCappedResponses() async throws {
        // Insert a valid free box before moov, leaving sample offsets intact. This
        // puts required movie metadata beyond a small response cap without a large fixture.
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
            XCTAssertEqual(request.value(forHTTPHeaderField: "Accept-Encoding"), "identity")
            let range = try XCTUnwrap(request.value(forHTTPHeaderField: "Range"))
            let bounds = range.dropFirst(6).split(separator: "-")
            let start = try XCTUnwrap(Int(bounds[0]))
            let requestedEnd = try XCTUnwrap(Int(bounds[1]))
            guard start < content.count else { return (416, [:], Data()) }
            XCTAssertLessThanOrEqual(requestedEnd - start + 1, MediaRange.chunkSize)
            // Also tolerate a server/proxy cap smaller than the requested range.
            let end = min(requestedEnd, start + 65535, content.count - 1)
            lock.lock(); offsets.append(start); lock.unlock()
            return (206, ["Content-Type": "video/mp4", "Content-Range": "bytes \(start)-\(end)/\(content.count)"],
                    content.subdata(in: start..<end + 1))
        }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MediaProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Fixture", url: URL(string: "https://files.example/companion")!),
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
