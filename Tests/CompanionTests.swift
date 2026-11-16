import XCTest
@testable import myFiles

final class CompanionTests: XCTestCase {
    func testExistingConnectionsDecodeWithoutCompanion() throws {
        let data = Data(#"{"id":"11111111-1111-1111-1111-111111111111","name":"Old","url":"https://files.example"}"#.utf8)
        let connection = try JSONDecoder().decode(Connection.self, from: data)
        XCTAssertNil(connection.companionURL)
        XCTAssertNil(connection.companionTokenID)
    }
    func testProtectedPathsUseComponentBoundaries() {
        XCTAssertTrue(CompanionClient.protected("/Trash"))
        XCTAssertTrue(CompanionClient.protected("/Trash/id/file"))
        XCTAssertTrue(CompanionClient.protected("/.filebeans-stage/id"))
        XCTAssertFalse(CompanionClient.protected("/Trashcan"))
        XCTAssertFalse(CompanionClient.protected("/Videos/Trash"))
    }
    func testJobActivityAndMissingProgress() throws {
        let job = try JSONDecoder().decode(CopyJob.self, from: Data(#"{"id":"fixture","state":"waiting","total":0,"done":0,"speed":0,"current":"","error":"Share unavailable"}"#.utf8))
        XCTAssertTrue(job.active)
        XCTAssertEqual(job.done, 0)
    }
}

private final class CompanionStub: URLProtocol {
    static var inspect: ((URLRequest) throws -> (Int, Data))?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (status, data) = try Self.inspect!(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class CompanionTransportTests: XCTestCase {
    private func client() -> CompanionClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [CompanionStub.self]
        return CompanionClient(connection: Connection(name: "Fixture", url: URL(string: "https://quantum.example")!,
                                                       companionURL: URL(string: "https://files.example/companion")!, companionTokenID: UUID()),
                               credential: "synthetic-companion-token", configuration: config)
    }
    override func tearDown() { CompanionStub.inspect = nil }
    func testProtectedDeleteUsesCompanionNotQuantum() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/companion/v1/trash")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-companion-token")
            XCTAssertNotNil(request.value(forHTTPHeaderField: "Idempotency-Key"))
            XCTAssertFalse(request.url!.absoluteString.contains("synthetic-companion-token"))
            return (201, Data(#"{"state":"retained"}"#.utf8))
        }
        let client = client(); defer { client.close() }
        let item = StoredItem(id: "fixture", parent: "root", source: "samba",
                              file: RemoteFile(name: "video.mp4", path: "/video.mp4", type: "video/mp4"), permissions: Permissions(delete: true))
        try await client.trash(item)
    }
    func testServerErrorIsSurfacedWithoutFallback() async throws {
        CompanionStub.inspect = { _ in (503, Data(#"{"error":"Samba unavailable"}"#.utf8)) }
        let client = client(); defer { client.close() }
        do {
            _ = try await client.request("trash", method: "POST")
            XCTFail("Service failure was ignored")
        } catch { XCTAssertEqual(error.localizedDescription, "Samba unavailable") }
    }
    func testPermanentRemovalRequiresExplicitHeader() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.value(forHTTPHeaderField: "X-Confirm-Permanent"), "yes")
            return (200, Data(#"{"purged":true}"#.utf8))
        }
        let client = client(); defer { client.close() }
        _ = try await client.request("trash/fixture/purge", method: "POST", permanent: true)
    }
}
