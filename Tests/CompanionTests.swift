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
    func testQueuedJobShowsNamesAndDestinationBeforeDiscovery() throws {
        let data = Data(#"{"id":"queued","state":"queued","total":0,"done":0,"speed":0,"current":"","cancel":true,"request":{"items":[{"source":"drive","path":"opaque-id","name":"video.mp4"}],"destination":{"source":"samba","path":"Videos"}}}"#.utf8)
        let job = try JSONDecoder().decode(CopyJob.self, from: data)
        XCTAssertEqual(job.title, "video.mp4")
        XCTAssertEqual(job.request?.destination.displayLocation, "samba: Videos")
        XCTAssertEqual(job.cancel, true)
    }
}

private final class CompanionStub: URLProtocol {
    static var inspect: ((URLRequest) throws -> (Int, Data))?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            var inspected = request
            if inspected.httpBody == nil, let stream = inspected.httpBodyStream {
                stream.open(); defer { stream.close() }
                var data = Data()
                var buffer = [UInt8](repeating: 0, count: 4096)
                while true {
                    let count = stream.read(&buffer, maxLength: buffer.count)
                    if count < 0 { throw stream.streamError ?? URLError(.cannotDecodeRawData) }
                    if count == 0 { break }
                    data.append(contentsOf: buffer.prefix(count))
                }
                inspected.httpBody = data
            }
            let (status, data) = try Self.inspect!(inspected)
            let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class CompanionTransportTests: XCTestCase {
    private let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    private func client(scope: String = "/") throws -> CompanionClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [CompanionStub.self]
        let connection = Connection(name: "Fixture", url: URL(string: "https://quantum.example")!,
                                    companionURL: URL(string: "https://files.example/companion")!, companionTokenID: UUID())
        let store = SharedStore(directory: directory)
        try store.updateScope(connection: connection.id, source: "samba", boundary: scope)
        return CompanionClient(connection: connection, credential: "synthetic-companion-token", configuration: config, store: store)
    }


    func testMultipleMovesUseOneSubmissionWithEverySelectedFile() async throws {
        var submissions = 0
        CompanionStub.inspect = { request in
            submissions += 1
            XCTAssertEqual(request.url?.path, "/companion/v1/jobs")
            let body = try JSONSerialization.jsonObject(with: request.httpBody!) as! [String: Any]
            XCTAssertEqual(body["operation"] as? String, "move")
            let items = body["items"] as! [[String: Any]]
            XCTAssertEqual(items.compactMap { $0["path"] as? String }, ["/Documents/a", "/Documents/b", "/Documents/c"])
            return (202, Data(#"{"id":"batch","state":"queued","total":0,"done":0,"speed":0,"current":""}"#.utf8))
        }
        let service = try client(scope: "/Documents"); defer { service.close() }
        _ = try await service.submit(BackgroundCopy(items: ["c","b","a"].map { CopyLocation(source: "samba", path: "/" + $0) },
            destination: CopyLocation(source: "samba", path: "/Target"), operation: "move"))
        XCTAssertEqual(submissions, 1)
    }
    func testMoveSubmissionPreservesOperationNameAndSourceScope() async throws {
        CompanionStub.inspect = { request in
            let body = try JSONSerialization.jsonObject(with: request.httpBody!) as! [String: Any]
            XCTAssertEqual(body["operation"] as? String, "move")
            XCTAssertEqual(body["destinationName"] as? String, "renamed.mov")
            let items = body["items"] as! [[String: Any]]
            XCTAssertEqual(items[0]["path"] as? String, "/allowed/movie.mov")
            return (202, Data(#"{"id":"move-1","state":"queued","total":0,"done":0,"speed":0,"current":""}"#.utf8))
        }
        let service = try client(scope: "/allowed"); defer { service.close() }
        _ = try await service.submit(BackgroundCopy(items: [CopyLocation(source: "samba", path: "/movie.mov")],
            destination: CopyLocation(source: "samba", path: "/Private"), operation: "move", destinationName: "renamed.mov"))
    }
    func testCopySubmissionKeepsLegacyPayload() async throws {
        CompanionStub.inspect = { request in
            let body = try JSONSerialization.jsonObject(with: request.httpBody!) as! [String: Any]
            XCTAssertNil(body["operation"]); XCTAssertNil(body["destinationName"])
            return (202, Data(#"{"id":"copy-1","state":"queued","total":0,"done":0,"speed":0,"current":""}"#.utf8))
        }
        let service = try client(); defer { service.close() }
        _ = try await service.submit(BackgroundCopy(items: [CopyLocation(source: "samba", path: "/movie.mov")],
            destination: CopyLocation(source: "samba", path: "/Private")))
    }
    func testCommittedMoveDisablesCancellation() throws {
        let job = try JSONDecoder().decode(CopyJob.self, from: Data(#"{"id":"move-1","state":"copying","total":1,"done":1,"speed":0,"current":"","canCancel":false,"request":{"operation":"move","items":[],"destination":{"source":"samba","path":"/Private"}}}"#.utf8))
        XCTAssertEqual(job.canCancel, false)
        XCTAssertEqual(job.title, "Server move")
    }
    override func tearDown() { CompanionStub.inspect = nil; try? FileManager.default.removeItem(at: directory) }
    func testProtectedCreationPreservesIdempotencyAndSendsKeyOnlyInBody() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/companion/v1/vaults")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Idempotency-Key"), "synthetic-create-request")
            XCTAssertFalse(request.url!.absoluteString.contains("synthetic-key"))
            let body = try JSONDecoder().decode([String: String].self, from: XCTUnwrap(request.httpBody))
            XCTAssertEqual(body, ["source": "samba", "parent": "/Photos", "name": "Private", "key": "synthetic-key"])
            return (201, Data(#"{"id":"fixture","source":"samba","path":"/Photos/Private","name":"Private","state":"unlocked","initialized":true}"#.utf8))
        }
        let client = try client(); defer { client.close() }
        let vault = try await client.createProtectedFolder(source: "samba", parent: "/Photos", name: "Private",
                                                          code: "synthetic-key", requestID: "synthetic-create-request")
        XCTAssertEqual(vault.path, "/Photos/Private")
        XCTAssertEqual(vault.state, "unlocked")
    }
    func testLegacyInitializeKeepsExplicitConfirmationHeader() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.url?.path, "/companion/v1/vaults/fixture/initialize")
            XCTAssertEqual(request.value(forHTTPHeaderField: "X-Confirm-Permanent"), "yes")
            return (200, Data("[]".utf8))
        }
        let client = try client(); defer { client.close() }
        try await client.vaultAction(id: "fixture", action: "initialize", code: "synthetic-key")
    }
    func testProtectedDeleteUsesCompanionNotQuantum() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/companion/v1/trash")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-companion-token")
            XCTAssertNotNil(request.value(forHTTPHeaderField: "Idempotency-Key"))
            XCTAssertFalse(request.url!.absoluteString.contains("synthetic-companion-token"))
            return (201, Data(#"{"state":"retained"}"#.utf8))
        }
        let client = try client(); defer { client.close() }
        let item = StoredItem(id: "fixture", parent: "root", source: "samba",
                              file: RemoteFile(name: "video.mp4", path: "/video.mp4", type: "video/mp4"), permissions: Permissions(delete: true))
        try await client.trash(item)
    }
    func testServerErrorIsSurfacedWithoutFallback() async throws {
        CompanionStub.inspect = { _ in (503, Data(#"{"error":"Samba unavailable"}"#.utf8)) }
        let client = try client(); defer { client.close() }
        do {
            _ = try await client.request("trash", method: "POST")
            XCTFail("Service failure was ignored")
        } catch { XCTAssertEqual(error.localizedDescription, "Samba unavailable") }
    }
    func testProtectedDeleteResolvesTheAuthenticatedSourceScope() async throws {
        CompanionStub.inspect = { request in
            let body = try JSONDecoder().decode([String: String].self, from: XCTUnwrap(request.httpBody))
            XCTAssertEqual(body["source"], "samba")
            XCTAssertEqual(body["path"], "/Documents/Private/video.mp4")
            return (201, Data(#"{"state":"retained"}"#.utf8))
        }
        let client = try client(scope: "/Documents"); defer { client.close() }
        let item = StoredItem(id: "fixture", parent: "root", source: "samba",
                              file: RemoteFile(name: "video.mp4", path: "/Private/video.mp4", type: "video/mp4"),
                              permissions: Permissions(delete: true))
        try await client.trash(item)
    }
    func testDrivePageTokenEscapesLiteralPlus() async throws {
        CompanionStub.inspect = { request in
            let parts = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)!
            XCTAssertTrue(parts.percentEncodedQuery!.contains("a%2Bb"))
            XCTAssertEqual(parts.queryItems?.first(where: { $0.name == "page" })?.value, "a+b")
            return (200, Data("{}".utf8))
        }
        let client = try client(); defer { client.close() }
        _ = try await client.request("drive/files", query: ["folder": "root", "page": "a+b"])
    }
    func testPermanentRemovalRequiresExplicitHeader() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.value(forHTTPHeaderField: "X-Confirm-Permanent"), "yes")
            return (200, Data(#"{"purged":true}"#.utf8))
        }
        let client = try client(); defer { client.close() }
        _ = try await client.request("trash/fixture/purge", method: "POST", permanent: true)
    }
}
