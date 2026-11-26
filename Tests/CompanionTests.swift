import XCTest
@testable import myFiles

final class CompanionTests: XCTestCase {
    func testExistingConnectionsDecodeWithoutCompanion() throws {
        let data = Data(#"{"id":"11111111-1111-1111-1111-111111111111","name":"Old","url":"https://files.example"}"#.utf8)
        let connection = try JSONDecoder().decode(Connection.self, from: data)
        XCTAssertNil(connection.companionURL)
        XCTAssertNil(connection.companionTokenID)
    }
    func testLegacyCompanionBecomesFilesystemEndpointWithoutChangingDomain() throws {
        let id = UUID(), credential = UUID()
        let old = Connection(id: id, name: "Files", url: URL(string: "https://files.example/quantum")!,
                             companionURL: URL(string: "https://files.example/companion")!, companionTokenID: credential)
        let current = old.unified
        XCTAssertEqual(current.id, id)
        XCTAssertEqual(current.url, old.url)
        XCTAssertEqual(current.filesystemURL, old.companionURL)
        XCTAssertEqual(current.filesystemTokenID, credential)
        let direct = Connection(name: "Direct", url: URL(string: "https://files.example/api")!)
        XCTAssertEqual(direct.unified.companionURL, direct.url)
        XCTAssertEqual(direct.unified.companionTokenID, direct.id)
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

private actor JobRetryGate {
    private var released = false
    private var continuation: CheckedContinuation<Void, Never>?
    func wait() async {
        guard !released else { return }
        await withCheckedContinuation { continuation = $0 }
    }
    func resume() {
        released = true
        continuation?.resume(); continuation = nil
    }
}

final class CompanionTransportTests: XCTestCase {
    private let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    private func client(scope: String = "/", connection existing: Connection? = nil) throws -> CompanionClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [CompanionStub.self]
        let connection = existing ?? Connection(name: "Fixture", url: URL(string: "https://quantum.example")!,
                                    companionURL: URL(string: "https://files.example/companion")!, companionTokenID: UUID())
        let store = SharedStore(directory: directory)
        try store.updateScope(connection: connection.id, source: "samba", boundary: scope)
        return CompanionClient(connection: connection, credential: "synthetic-companion-token", configuration: config, store: store)
    }


    func testHostingUsesCanonicalSourcePathAndFilenameEndingLink() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.httpMethod, "POST")
            XCTAssertEqual(request.url?.path, "/companion/v1/hosting")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer synthetic-companion-token")
            let body = try JSONSerialization.jsonObject(with: request.httpBody!) as! [String: Any]
            let item = body["item"] as! [String: Any]
            XCTAssertEqual(item["source"] as? String, "samba")
            XCTAssertEqual(item["path"] as? String, "/Documents/旅行 + report.pdf")
            XCTAssertEqual(body["expires"] as? Int, 0)
            return (201, Data(#"{"id":"report","name":"旅行 + report.pdf","created":1800000000,"expires":0,"state":"active","urlPath":"/h/report/%E6%97%85%E8%A1%8C%20+%20report.pdf"}"#.utf8))
        }
        let service = try client(scope: "/Documents"); defer { service.close() }
        let hosted = try await service.hostFile(CopyLocation(source: "samba", path: "/旅行 + report.pdf"), expires: 0)
        let url = try XCTUnwrap(hosted.url(connection: service.connection))
        XCTAssertEqual(url.host, "files.example")
        XCTAssertEqual(url.path, "/companion/h/report/旅行 + report.pdf")
        XCTAssertEqual(url.lastPathComponent, "旅行 + report.pdf")
        XCTAssertNil(URLComponents(url: url, resolvingAgainstBaseURL: false)?.query)
        XCTAssertFalse(url.absoluteString.contains("synthetic-companion-token"))
    }

    func testHostingManagementKeepsLinkStableWhenChangingDuration() async throws {
        var methods: [String] = []
        CompanionStub.inspect = { request in
            methods.append(request.httpMethod!)
            XCTAssertEqual(request.url?.path, "/companion/v1/hosting/report")
            if request.httpMethod == "DELETE" { return (204, Data()) }
            let body = try JSONSerialization.jsonObject(with: request.httpBody!) as! [String: Any]
            XCTAssertEqual(body["expires"] as? Int64, 1900000000)
            return (200, Data(#"{"id":"report","name":"report.pdf","created":1800000000,"expires":1900000000,"state":"active","urlPath":"/h/report/report.pdf"}"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let hosted = try await service.updateHosting("report", expires: 1900000000)
        XCTAssertEqual(hosted.url(connection: service.connection)?.absoluteString, "https://files.example/companion/h/report/report.pdf")
        try await service.stopHosting("report")
        XCTAssertEqual(methods, ["PATCH", "DELETE"])
    }

    func testSambaTrashUsesSourceQueryAndDecodesRetainedItems() async throws {
        CompanionStub.inspect = { request in
            XCTAssertEqual(request.url?.path, "/companion/v1/trash")
            XCTAssertEqual(URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems?.first(where: { $0.name == "source" })?.value, "Samba")
            return (200, Data(#"[{"id":"retained","source":"Samba","original":"Folder/video.mp4","name":"video.mp4","deleted":1800000000,"expires":1800864000,"state":"retained","vault":"opaque"}]"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let entries = try await service.trashEntries(source: "Samba")
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries.first?.name, "video.mp4")
        XCTAssertEqual(entries.first?.state, "retained")
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
    func testInterruptedSubmissionWaitsForReconnectAndReturnsCompletedJobWithSameKey() async throws {
        var requests: [URLRequest] = []
        CompanionStub.inspect = { request in
            requests.append(request)
            if requests.count == 1 { throw URLError(.timedOut) }
            return (200, Data(#"{"id":"already-finished","state":"completed","total":100,"done":100,"speed":0,"current":""}"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let reconnect = expectation(description: "Waiting for foreground reconnect")
        let gate = JobRetryGate()
        service.prepareForJobRetry = { reconnect.fulfill(); await gate.wait() }
        let submission = Task {
            try await service.submit(BackgroundCopy(items: [CopyLocation(source: "samba", path: "/movie.mov")],
                destination: CopyLocation(source: "samba", path: "/Target"), operation: "move"))
        }
        await fulfillment(of: [reconnect], timeout: 2)
        XCTAssertEqual(requests.count, 1, "Do not reconnect while the app is inactive")
        await gate.resume()
        let job = try await submission.value
        XCTAssertEqual(job.id, "already-finished")
        XCTAssertEqual(job.state, "completed")
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests[0].httpBody, requests[1].httpBody)
        XCTAssertNotNil(requests[0].value(forHTTPHeaderField: "Idempotency-Key"))
        XCTAssertEqual(requests[0].value(forHTTPHeaderField: "Idempotency-Key"), requests[1].value(forHTTPHeaderField: "Idempotency-Key"))
        XCTAssertTrue(try FileManager.default.contentsOfDirectory(atPath: directory.appendingPathComponent("copy-submissions").path).isEmpty)
    }
    func testRepeatedTimeoutRemainsAnErrorAndPreservesRequestKeyAcrossClientRestart() async throws {
        var keys: [String?] = []
        CompanionStub.inspect = { request in
            keys.append(request.value(forHTTPHeaderField: "Idempotency-Key"))
            if keys.count <= 2 { throw URLError(.timedOut) }
            return (200, Data(#"{"id":"recovered","state":"completed","total":1,"done":1,"speed":0,"current":""}"#.utf8))
        }
        let service = try client()
        let copy = BackgroundCopy(items: [CopyLocation(source: "samba", path: "/movie.mov")],
            destination: CopyLocation(source: "samba", path: "/Target"))
        do { _ = try await service.submit(copy); XCTFail("A persistent connection failure must remain visible") }
        catch { XCTAssertEqual((error as? URLError)?.code, .timedOut) }
        XCTAssertEqual(keys.count, 2, "Reconnect must be bounded")
        service.close()
        let restarted = try client(connection: service.connection); defer { restarted.close() }
        let job = try await restarted.submit(copy)
        XCTAssertEqual(job.state, "completed")
        XCTAssertEqual(keys.count, 3)
        XCTAssertNotNil(keys[0])
        XCTAssertEqual(keys[0], keys[1]); XCTAssertEqual(keys[0], keys[2])
    }
    func testJobStatusReconnectsWithoutResubmittingTheOperation() async throws {
        var calls = 0
        CompanionStub.inspect = { request in
            calls += 1
            XCTAssertEqual(request.httpMethod, "GET")
            XCTAssertEqual(request.url?.path, "/companion/v1/jobs")
            if calls == 1 { throw URLError(.networkConnectionLost) }
            return (200, Data(#"[{"id":"finished","state":"completed","total":1,"done":1,"speed":0,"current":""}]"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let jobs = try await service.jobs()
        XCTAssertEqual(jobs.first?.state, "completed")
        XCTAssertEqual(calls, 2)
    }
    func testRejectedSubmissionDoesNotReconnect() async throws {
        var calls = 0
        CompanionStub.inspect = { _ in calls += 1; return (403, Data(#"{"error":"Permission denied"}"#.utf8)) }
        let service = try client(); defer { service.close() }
        service.prepareForJobRetry = { XCTFail("A permission failure must not be retried") }
        do {
            _ = try await service.submit(BackgroundCopy(items: [CopyLocation(source: "samba", path: "/movie.mov")],
                destination: CopyLocation(source: "samba", path: "/Target")))
            XCTFail("Permission failure was hidden")
        } catch { XCTAssertEqual((error as NSError).code, 403) }
        XCTAssertEqual(calls, 1)
    }
    func testMoveWaitReconnectsToTheKnownCompletedJob() async throws {
        var calls = 0
        CompanionStub.inspect = { request in
            calls += 1
            XCTAssertEqual(request.httpMethod, "GET")
            XCTAssertEqual(request.url?.path, "/companion/v1/jobs/move-1")
            if calls == 1 { throw URLError(.timedOut) }
            return (200, Data(#"{"id":"move-1","state":"completed","total":1,"done":1,"speed":0,"current":""}"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let queued = try JSONDecoder().decode(CopyJob.self, from: Data(#"{"id":"move-1","state":"queued","total":1,"done":0,"speed":0,"current":""}"#.utf8))
        try await service.waitForMove(queued)
        XCTAssertEqual(calls, 2)
    }
    func testArchiveStatusReconnectsAfterInterruptedRequest() async throws {
        var calls = 0
        CompanionStub.inspect = { request in
            calls += 1
            XCTAssertEqual(request.httpMethod, "GET")
            XCTAssertEqual(request.url?.path, "/companion/v1/archive-sync/status")
            if calls == 1 { throw URLError(.networkConnectionLost) }
            return (200, Data(#"{"pendingOriginals":0,"pendingChanges":0,"problems":0,"pendingWork":1,"items":[]}"#.utf8))
        }
        let service = try client(); defer { service.close() }
        let status = try await service.archiveSyncStatus()
        XCTAssertTrue(status.hasPending)
        XCTAssertEqual(calls, 2)
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
