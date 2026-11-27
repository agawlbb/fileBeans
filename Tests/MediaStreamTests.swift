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
    func testPagingAwayStopsACachedMovieWithoutEvictingIt() async throws {
        let fixture = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "playback-probe", withExtension: "mp4"))
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MediaProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Fixture", url: URL(string: "https://files.example")!),
                                      token: "synthetic-token", configuration: config)
        defer { client.close() }
        let request = try client.request("content", query: ["path": "/probe.mp4", "source": "disk"])
        let player = AVPlayer(playerItem: AVPlayerItem(url: fixture))
        let movie = Movie(name: "probe.mp4", stream: MediaStream(client: client, request: request, name: "probe.mp4"), player: player)
        let file = FolderViewerFile(id: "video", name: "probe.mp4", media: true) { .movie(movie) }
        let next = FolderViewerFile(id: "notes", name: "notes.txt") { throw URLError(.fileDoesNotExist) }
        let cache = FolderViewerCache(files: [file, next])
        defer { cache.clear() }
        cache.select(file.id)
        for _ in 0..<100 where cache.contents[file.id] == nil {
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTAssertNotNil(cache.contents[file.id])
        XCTAssertNotNil(player.currentItem)
        cache.select(next.id)
        XCTAssertNotNil(cache.contents[file.id], "The previous video remains cached as a neighbor")
        XCTAssertNil(player.currentItem, "Paging away, rather than presentation callbacks, stops playback")
    }

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
        defer { stream.cancel(); stream.clearCache() }
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
        stream.cancel()
        _ = try await stream.readChunk(offset: 270000, length: 32000)
        XCTAssertEqual(ranges.count, 2, "A backward seek must reuse non-opening ranges after paging away")
        stream.clearCache()
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

        // Scrubbing repeatedly used to precede the persistent black-screen
        // failure. Seek while playing, without a pause/play workaround.
        let originalItem = player.currentItem
        for target in [2.0, 5.0, 1.0, 4.0, 2.0] {
            let seekStarted = Date()
            let seek = expectation(description: "Repeated seek to \(target)")
            player.seek(to: CMTime(seconds: target, preferredTimescale: 600), toleranceBefore: .zero, toleranceAfter: .zero) { finished in
                XCTAssertTrue(finished)
                seek.fulfill()
            }
            await fulfillment(of: [seek], timeout: 10)
            let deadline = Date().addingTimeInterval(10)
            var decoded = false
            while Date() < deadline {
                let time = player.currentTime()
                if time.seconds >= target + 0.5,
                   output.copyPixelBuffer(forItemTime: time, itemTimeForDisplay: nil) != nil {
                    decoded = true
                    break
                }
                try await Task.sleep(for: .milliseconds(100))
            }
            XCTAssertTrue(decoded, "Video must decode and advance after repeated seeks without manual resume")
            let seekLatency = Date().timeIntervalSince(seekStarted)
            print("Seek-to-moving-frame \(fixtureName) target=\(target)s latency=\(seekLatency)s")
            XCTAssertLessThan(seekLatency, 4, "Delayed-fixture seeks must resume promptly")
            XCTAssertTrue(player.currentItem === originalItem)
        }

        let movie = Movie(name: fixtureName, stream: stream, player: player)
        player.pause()
        movie.resumeBufferedPlayback()
        XCTAssertEqual(player.rate, 0, "Buffer recovery must preserve an intentional pause")
        try await movie.playIfNeeded()
        XCTAssertTrue(player.currentItem === originalItem, "Redisplaying a movie must retain its item")
        XCTAssertEqual(player.rate, 0, "Redisplaying a paused movie must not start it")
        movie.stop()
        XCTAssertNil(player.currentItem, "Removing the player must release its item")
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
