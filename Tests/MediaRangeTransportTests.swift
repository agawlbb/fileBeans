import XCTest
@testable import myFiles

private final class ProgressiveMediaProtocol: URLProtocol {
    static var start: ((ProgressiveMediaProtocol) -> Void)?
    static var stopped: (() -> Void)?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() { Self.start?(self) }
    override func stopLoading() { Self.stopped?() }
    func headers(status: Int = 206, range: String) {
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1",
                                      headerFields: ["Content-Type": "video/mp4", "Content-Range": range])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
    }
    func send(_ data: Data) { client?.urlProtocol(self, didLoad: data) }
    func finish() { client?.urlProtocolDidFinishLoading(self) }
}

@MainActor
final class MediaRangeTransportTests: XCTestCase {
    override func tearDown() { ProgressiveMediaProtocol.start = nil; ProgressiveMediaProtocol.stopped = nil }

    private func configuration() -> URLSessionConfiguration {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ProgressiveMediaProtocol.self]
        return configuration
    }

    func testFilesystemAndDriveDeliverBytesBeforeResponseCompletes() async throws {
        for drive in [false, true] {
            let config = configuration()
            let connection = Connection(name: "Fixture", url: URL(string: "https://files.example")!)
            let filesystem = FilesystemClient(connection: connection, token: "synthetic-token", configuration: config)
            let companion = CompanionClient(connection: connection, credential: "synthetic-token", configuration: config)
            defer { filesystem.close(); companion.close() }
            var request = URLRequest(url: URL(string: "https://files.example/video")!)
            request.setValue("Bearer synthetic-token", forHTTPHeaderField: "Authorization")
            let delivered = expectation(description: "Initial bytes available while response is still open")
            let started = Date()
            var firstDelivery: TimeInterval?
            var driver: ProgressiveMediaProtocol?
            ProgressiveMediaProtocol.start = { source in
                Task { @MainActor in
                    XCTAssertEqual(source.request.value(forHTTPHeaderField: "Range"), "bytes=0-1048575")
                    XCTAssertEqual(source.request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-token")
                    driver = source
                    source.headers(range: "bytes 0-1048575/2097152")
                    source.send(Data(repeating: 1, count: 65536))
                }
            }
            let task = Task { @MainActor in
                var data = Data()
                let chunks = drive ? companion.streamChunks(request, offset: 0, length: 1 << 20)
                    : filesystem.streamChunks(request, offset: 0, length: 1 << 20)
                for try await chunk in chunks {
                    if data.isEmpty { firstDelivery = Date().timeIntervalSince(started); delivered.fulfill() }
                    data.append(chunk.data)
                }
                return data
            }
            defer { task.cancel() }
            await fulfillment(of: [delivered], timeout: 3)
            XCTAssertNotNil(firstDelivery)
            // The old data(for:) path could not supply anything during this held response.
            try await Task.sleep(for: .milliseconds(200))
            let beforeCompletion = Date().timeIntervalSince(started)
            driver?.send(Data(repeating: 2, count: (1 << 20) - 65536)); driver?.finish()
            let data = try await task.value
            XCTAssertEqual(data.count, 1 << 20)
            XCTAssertLessThan(try XCTUnwrap(firstDelivery), beforeCompletion)
            print("Media delivery \(drive ? "Drive" : "filesystem"): first bytes \(firstDelivery ?? -1)s; whole-response baseline >= \(beforeCompletion)s")
        }
    }

    func testInvalidHeadersNeverDeliverBytesAndTruncationFails() async throws {
        for (status, range, count) in [(200, "bytes 0-3/10", 4), (206, "bytes 1-4/10", 4), (206, "bytes 0-3/10", 2)] {
            let transport = MediaRangeTransport(configuration: configuration())
            defer { transport.close() }
            ProgressiveMediaProtocol.start = { source in
                source.headers(status: status, range: range)
                source.send(Data(repeating: 1, count: count)); source.finish()
            }
            var received = 0
            do {
                for try await chunk in transport.chunks(URLRequest(url: URL(string: "https://files.example/video")!), offset: 0, length: 4) {
                    received += chunk.data.count
                }
                XCTFail("Invalid response must fail")
            } catch { XCTAssertEqual((error as? URLError)?.code, .badServerResponse) }
            if status == 200 || range.hasPrefix("bytes 1-") { XCTAssertEqual(received, 0) }
        }
    }

    func testCancellingConsumerStopsTheNetworkRequest() async throws {
        let transport = MediaRangeTransport(configuration: configuration())
        defer { transport.close() }
        let started = expectation(description: "Request started")
        let stopped = expectation(description: "Cancelled request stopped")
        ProgressiveMediaProtocol.start = { source in
            source.headers(range: "bytes 0-999/1000"); started.fulfill()
        }
        ProgressiveMediaProtocol.stopped = { stopped.fulfill() }
        let task = Task {
            for try await _ in transport.chunks(URLRequest(url: URL(string: "https://files.example/video")!), offset: 0, length: 1000) { }
        }
        await fulfillment(of: [started], timeout: 3)
        task.cancel()
        _ = await task.result
        await fulfillment(of: [stopped], timeout: 3)
    }
}
