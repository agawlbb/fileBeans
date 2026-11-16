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
    private func client() -> QuantumClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubProtocol.self]
        return QuantumClient(connection: Connection(name: "Test", url: URL(string: "https://files.example.com/quantum")!),
                             token: "synthetic-api-token", configuration: config)
    }
    override func tearDown() { StubProtocol.response = nil }
    func testMoveNeverRequestsOverwriteAndPreservesSource() async throws {
        StubProtocol.response = { request in
            XCTAssertEqual(request.httpMethod, "PATCH")
            XCTAssertEqual(request.url?.path, "/quantum/api/resources")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
            var body = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 4096)
                while stream.hasBytesAvailable {
                    let read = stream.read(&buffer, maxLength: buffer.count)
                    if read <= 0 { break }
                    body.append(contentsOf: buffer.prefix(read))
                }
            }
            let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(json["overwrite"] as? Bool, false)
            XCTAssertEqual(json["rename"] as? Bool, false)
            let items = try XCTUnwrap(json["items"] as? [[String: String]])
            XCTAssertEqual(items[0]["fromSource"], "disk")
            XCTAssertEqual(items[0]["toSource"], "disk")
            XCTAssertEqual(items[0]["toPath"], "/new/a & b.txt")
            return (200, Data())
        }
        let client = client(); defer { client.close() }
        try await client.move(source: "disk", from: "/a & b.txt", to: "/new/a & b.txt")
    }
    func testCopySendsOnlyServerPathsWithoutOverwrite() async throws {
        StubProtocol.response = { request in
            XCTAssertEqual(request.httpMethod, "PATCH")
            XCTAssertEqual(request.url?.path, "/quantum/api/resources")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
            var body = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 4096)
                while stream.hasBytesAvailable {
                    let read = stream.read(&buffer, maxLength: buffer.count)
                    if read <= 0 { break }
                    body.append(contentsOf: buffer.prefix(read))
                }
            }
            let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(json["action"] as? String, "copy")
            XCTAssertEqual(json["overwrite"] as? Bool, false)
            XCTAssertEqual(json["rename"] as? Bool, false)
            let items = try XCTUnwrap(json["items"] as? [[String: String]])
            XCTAssertEqual(items.count, 1)
            XCTAssertEqual(items[0]["fromPath"], "/a & b.txt")
            XCTAssertEqual(items[0]["fromSource"], "disk")
            XCTAssertEqual(items[0]["toSource"], "disk")
            XCTAssertEqual(items[0]["toPath"], "/new/a & b.txt")
            return (200, Data())
        }
        let client = client(); defer { client.close() }
        try await client.copy(source: "disk", from: "/a & b.txt", toSource: "disk", to: "/new/a & b.txt")
    }
    func testCopyBetweenSourcesSendsDistinctSourcesEvenForIdenticalPaths() async throws {
        StubProtocol.response = { request in
            XCTAssertEqual(request.httpMethod, "PATCH")
            XCTAssertEqual(request.url?.path, "/quantum/api/resources")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
            var body = request.httpBody ?? Data()
            if let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buffer = [UInt8](repeating: 0, count: 4096)
                while stream.hasBytesAvailable {
                    let read = stream.read(&buffer, maxLength: buffer.count)
                    if read <= 0 { break }
                    body.append(contentsOf: buffer.prefix(read))
                }
            }
            let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
            XCTAssertEqual(json["action"] as? String, "copy")
            XCTAssertEqual(json["overwrite"] as? Bool, false)
            XCTAssertEqual(json["rename"] as? Bool, false)
            let items = try XCTUnwrap(json["items"] as? [[String: String]])
            XCTAssertEqual(items.count, 1)
            XCTAssertEqual(items[0]["fromPath"], "/a & b.txt")
            XCTAssertEqual(items[0]["fromSource"], "disk")
            XCTAssertEqual(items[0]["toSource"], "samba")
            XCTAssertEqual(items[0]["toPath"], "/a & b.txt")
            return (200, Data())
        }
        let client = client(); defer { client.close() }
        try await client.copy(source: "disk", from: "/a & b.txt", toSource: "samba", to: "/a & b.txt")
    }
    func testStreamingUsesScopedViewTokenInsteadOfAPITokenInURL() async throws {
        StubProtocol.response = { _ in
            (200, Data(#"{"name":"clip.mp4","path":"/clip.mp4","type":"video/mp4","viewToken":"scoped-view-token"}"#.utf8))
        }
        let client = client(); defer { client.close() }
        let request = try await client.streamRequest(source: "disk", path: "/clip.mp4")
        let url = try XCTUnwrap(request.url)
        XCTAssertEqual(url.path, "/quantum/api/media/stream")
        XCTAssertFalse(url.absoluteString.contains("synthetic-api-token"))
        let components = try XCTUnwrap(URLComponents(url: url, resolvingAgainstBaseURL: false))
        XCTAssertEqual(components.queryItems?.first { $0.name == "viewToken" }?.value, "scoped-view-token")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-api-token")
    }
    func testExpiredTokenBecomesActionableAuthenticationError() async throws {
        StubProtocol.response = { _ in (401, Data("private server details".utf8)) }
        let client = client(); defer { client.close() }
        do { _ = try await client.scopes(); XCTFail("Expected rejected token") }
        catch APIError.authentication { }
        catch { XCTFail("Unexpected error: \(error)") }
    }
}
