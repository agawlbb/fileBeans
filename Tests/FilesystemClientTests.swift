import XCTest
@testable import myFiles

private final class TextContentProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let headers = request.url?.lastPathComponent == "known-length" ? ["Content-Length": "8"] : [:]
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("markdown".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class FilesystemClientTests: XCTestCase {
    func testArchiveSyncStatusCountsWorkWithoutProtectedPaths() throws {
        let empty = try JSONDecoder().decode(ArchiveSyncStatus.self, from: Data(#"{"pendingOriginals":0,"pendingChanges":0,"problems":0}"#.utf8))
        XCTAssertFalse(empty.hasPending)
        for field in ["pendingOriginals", "pendingChanges", "problems"] {
            var counts = ["pendingOriginals":0, "pendingChanges":0, "problems":0]
            counts[field] = 1
            let status = try JSONDecoder().decode(ArchiveSyncStatus.self, from: JSONSerialization.data(withJSONObject: counts))
            XCTAssertTrue(status.hasPending)
        }
    }
    func testArchiveThumbnailRequestsOnlyExistingCache() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ArchiveThumbnailProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Test", url: URL(string: "https://files.example.com")!), token: "fixture", configuration: configuration)
        defer { client.close() }
        let file = RemoteFile(name: "photo.jpg", path: "/archive/sub/photo.jpg", type: "image/jpeg", hasPreview: true, archiveRole: "archive")
        let data = try await client.thumbnail(source: "samba", file: file)
        XCTAssertEqual(data, Data("cached-thumbnail".utf8))
    }
    func testArchiveWriteErrorPreservesMatchedFolderMessage() throws {
        let client = FilesystemClient(connection: Connection(name: "Test", url: URL(string: "https://files.example.com")!), token: "fixture")
        defer { client.close() }
        let response = HTTPURLResponse(url: URL(string: "https://files.example.com")!, statusCode: 403, httpVersion: nil, headerFields: nil)!
        let body = Data(#"{"error":"Make changes in VPS /pics."}"#.utf8)
        XCTAssertThrowsError(try client.check(response, body: body)) { error in
            XCTAssertEqual(error.localizedDescription, "Make changes in VPS /pics.")
        }
    }
    func testArchiveMetadataSurvivesResourceDecode() throws {
        let data = Data(#"{"name":"archive","path":"/archive","type":"directory","archiveLink":"link","archiveRole":"archive","archiveRoot":true,"archiveLibrary":{"source":"VPS","path":"/pics","available":true},"children":[{"name":"sub","path":"/archive/sub","type":"directory","archiveLink":"link","archiveRole":"archive"}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: data)
        XCTAssertEqual(resource.file.archiveRoot, true)
        XCTAssertEqual(resource.file.archiveLink, "link")
        XCTAssertEqual(resource.file.archiveLibrary?.path, "/pics")
        XCTAssertEqual(resource.children[0].archiveRole, "archive")
    }

    func testTextLoadingEnforcesLimitsWithAndWithoutContentLength() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [TextContentProtocol.self]
        let client = FilesystemClient(connection: Connection(name: "Test", url: URL(string: "https://files.example.com")!),
                                      token: "fixture-token", configuration: configuration)
        defer { client.close() }
        for path in ["known-length", "unknown-length"] {
            let request = URLRequest(url: URL(string: "https://files.example.com/" + path)!)
            let content = try await client.data(request, limit: 8)
            XCTAssertEqual(content, Data("markdown".utf8))
            do {
                _ = try await client.data(request, limit: 7)
                XCTFail("Oversized text response must be rejected")
            } catch { XCTAssertEqual((error as NSError).code, CocoaError.fileReadTooLarge.rawValue) }
        }
    }
    func testBasePathAndSpecialCharactersStayInQuery() throws {
        let url = try FilesystemClient.validateURL("https://files.example.com/companion/")
        let client = FilesystemClient(connection: Connection(name: "Test", url: url), token: "fixture-token")
        defer { client.close() }
        let request = try client.request("resources", query: ["source": "Photos & video", "path": "/旅行/a+b?#.jpg"])
        let components = try XCTUnwrap(URLComponents(url: XCTUnwrap(request.url), resolvingAgainstBaseURL: false))
        XCTAssertEqual(components.path, "/companion/v1/fs/resources")
        XCTAssertEqual(components.queryItems?.first { $0.name == "path" }?.value, "/旅行/a+b?#.jpg")
        XCTAssertTrue(components.percentEncodedQuery?.contains("%2B") == true)
        XCTAssertFalse(components.percentEncodedQuery?.contains("+") == true)
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer fixture-token")
        XCTAssertFalse(request.url!.absoluteString.contains("fixture-token"))
    }
    func testRejectsUnsafeConnectionURLs() {
        for value in ["http://files.example.com", "https://user:pass@files.example.com", "https://files.example.com?auth=secret", "https://files.example.com/#x", "files.example.com"] {
            XCTAssertThrowsError(try FilesystemClient.validateURL(value))
        }
    }
    func testDistinguishesConflictAuthenticationAndPartialMove() throws {
        let url = URL(string: "https://files.example.com")!
        let client = FilesystemClient(connection: Connection(name: "Test", url: url), token: "fixture")
        defer { client.close() }
        for code in [401, 403, 404, 409, 412, 207, 500] {
            XCTAssertThrowsError(try client.check(HTTPURLResponse(url: url, statusCode: code, httpVersion: nil, headerFields: nil)!))
        }
        XCTAssertNoThrow(try client.check(HTTPURLResponse(url: url, statusCode: 206, httpVersion: nil, headerFields: nil)!))
    }
    func testFilesystemV1FixtureAndLeastPrivilegeDefaults() throws {
        let data = Data(#"{"scopes":[{"name":"Documents","scope":"/personal","permissions":{"view":true,"download":true}}],"version":1}"#.utf8)
        let user = try JSONDecoder().decode(User.self, from: data)
        XCTAssertTrue(user.scopes[0].permissions.download)
        XCTAssertFalse(user.scopes[0].permissions.modify)
        XCTAssertThrowsError(try JSONDecoder().decode(User.self, from: Data(#"{"sourcePermissions":{}}"#.utf8)))
        let listing = Data(#"{"name":"personal","path":"/personal","type":"directory","children":[{"name":"旅行","path":"/personal/旅行","type":"directory"},{"name":"clip.mp4","path":"/personal/clip.mp4","type":"video/mp4","size":42,"modified":"2026-01-01T12:00:00.123Z","hasPreview":true}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.children.count, 2)
        XCTAssertTrue(resource.children[0].isDirectory)
        XCTAssertTrue(resource.children[1].isMedia)
        XCTAssertNotNil(resource.children[1].date)
    }
    func testChildrenHaveConfinedPaths() throws {
        // Every child carries a source-relative path, checked against its parent.
        let listing = Data(#"{"name":"photos","path":"/photos/","type":"directory","children":[{"name":"旅行","type":"directory","path":"/photos/旅行"},{"name":"a+b.mp4","type":"video/mp4","size":42,"path":"/photos/a+b.mp4","revision":"view-fixture"}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.children.map(\.path), ["/photos/旅行", "/photos/a+b.mp4"])
        XCTAssertTrue(resource.children[0].isDirectory)
        XCTAssertEqual(resource.children[1].size, 42)
        XCTAssertEqual(resource.children[1].revision, "view-fixture")
        let root = Data(#"{"name":"/","path":"/","type":"directory","children":[{"name":"docs","type":"directory","path":"/docs"}]}"#.utf8)
        XCTAssertEqual(try JSONDecoder().decode(Resource.self, from: root).children[0].path, "/docs")
        let invalid = Data(#"{"name":"/","path":"/","type":"directory","children":[{"name":"../escape","type":"text/plain","path":"/../escape"}]}"#.utf8)
        XCTAssertThrowsError(try JSONDecoder().decode(Resource.self, from: invalid))
        // Persisted catalog entries must still require their full path.
        XCTAssertThrowsError(try JSONDecoder().decode(RemoteFile.self, from: Data(#"{"name":"doc","type":"text/plain"}"#.utf8)))
    }
    func testNamesAndScopeContainment() throws {
        for name in ["", ".", "..", "a/b", "\0"] { XCTAssertThrowsError(try FilePath.child(name, in: "/")) }
        XCTAssertEqual(try FilePath.child("a & b", in: "/photos/"), "/photos/a & b")
        XCTAssertFalse(FilePath.contains("/photos-old", in: "/photos"))
        XCTAssertTrue(FilePath.contains("/photos/2026", in: "/photos"))
    }
    func testDirectoryTrailingSlashesDoNotChangeCatalogPaths() throws {
        let listing = Data(#"{"name":"photos","path":"/photos/","type":"directory","children":[{"name":"2026","path":"/photos/2026/","type":"directory"}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.file.path, "/photos")
        XCTAssertEqual(resource.children[0].path, try FilePath.child("2026", in: resource.file.path))
    }
}

private final class ArchiveThumbnailProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let components = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)
        guard request.httpMethod == "GET", request.httpBody == nil, request.httpBodyStream == nil,
              request.url?.lastPathComponent == "thumbnail",
              components?.queryItems?.contains(where: { $0.name == "cachedOnly" && $0.value == "true" }) == true else {
            client?.urlProtocol(self, didFailWithError: URLError(.badURL)); return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type":"image/jpeg"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("cached-thumbnail".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
