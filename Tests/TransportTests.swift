import XCTest
@testable import myFiles

private final class StubProtocol: URLProtocol {
    static var response: ((URLRequest) throws -> (Int, Data))?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (code, data) = try Self.response!(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: "HTTP/1.1",
                                           headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class TransportTests: XCTestCase {
    private func client() -> FilesystemClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubProtocol.self]
        return FilesystemClient(connection: Connection(name: "Test", url: URL(string: "https://files.example.com/companion")!),
                             token: "synthetic-api-token", configuration: config)
    }
    override func tearDown() { StubProtocol.response = nil }
    func testMoveUsesSourcePaths() async throws {
        StubProtocol.response = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/companion/v1/fs/move")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
            var data = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 4096)
                while stream.hasBytesAvailable {
                    let count = stream.read(&buffer, maxLength: buffer.count)
                    if count <= 0 { break }
                    data.append(contentsOf: buffer.prefix(count))
                }
            }
            let body = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: String])
            XCTAssertEqual(body, ["source": "disk", "from": "/a & b.txt", "to": "/new/a & b.txt"])
            return (204, Data())
        }
        let client = client(); defer { client.close() }
        try await client.move(source: "disk", from: "/a & b.txt", to: "/new/a & b.txt")
    }
    func testStreamingUsesHeaderAuthenticationWithoutViewGrant() async throws {
        StubProtocol.response = { _ in XCTFail("Streaming must not request a view grant"); return (500, Data()) }
        let client = client(); defer { client.close() }
        let request = try await client.streamRequest(source: "disk", path: "/clip.mp4")
        let url = try XCTUnwrap(request.url)
        XCTAssertEqual(url.path, "/companion/v1/fs/content")
        XCTAssertFalse(url.absoluteString.contains("synthetic-api-token"))
        let components = try XCTUnwrap(URLComponents(url: url, resolvingAgainstBaseURL: false))
        XCTAssertNil(components.queryItems?.first { $0.name == "viewToken" })
        XCTAssertEqual(components.queryItems?.first { $0.name == "path" }?.value, "/clip.mp4")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
    }
    func testSourcesRequireSupportedProtocolVersion() async throws {
        StubProtocol.response = { request in
            XCTAssertEqual(request.url?.path, "/companion/v1/fs/sources")
            return (200, Data(#"{"version":2,"scopes":[]}"#.utf8))
        }
        let client = client(); defer { client.close() }
        do { _ = try await client.scopes(); XCTFail("Unsupported protocol accepted") }
        catch APIError.incompatible { }
    }
    func testReplacementRequiresExpectedRevisionBeforeUpload() async throws {
        let client = client(); defer { client.close() }
        do {
            try await client.upload(source: "disk", path: "/doc", file: URL(fileURLWithPath: "/unused"), replacing: true)
            XCTFail("Unconditional replacement accepted")
        } catch APIError.conflict { }
    }
    func testExpiredTokenBecomesActionableAuthenticationError() async throws {
        StubProtocol.response = { _ in (401, Data("private server details".utf8)) }
        let client = client(); defer { client.close() }
        do { _ = try await client.scopes(); XCTFail("Expected rejected token") }
        catch APIError.authentication { }
        catch { XCTFail("Unexpected error: \(error)") }
    }
}
