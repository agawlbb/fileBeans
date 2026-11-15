import XCTest
@testable import myFiles

final class QuantumClientTests: XCTestCase {
    func testBasePathAndSpecialCharactersStayInQuery() throws {
        let url = try QuantumClient.validateURL("https://files.example.com/quantum/")
        let client = QuantumClient(connection: Connection(name: "Test", url: url), token: "fixture-token")
        defer { client.close() }
        let request = try client.request("resources", query: ["source": "Photos & video", "path": "/旅行/a+b?#.jpg"])
        let components = try XCTUnwrap(URLComponents(url: XCTUnwrap(request.url), resolvingAgainstBaseURL: false))
        XCTAssertEqual(components.path, "/quantum/api/resources")
        XCTAssertEqual(components.queryItems?.first { $0.name == "path" }?.value, "/旅行/a+b?#.jpg")
        XCTAssertTrue(components.percentEncodedQuery?.contains("%2B") == true)
        XCTAssertFalse(components.percentEncodedQuery?.contains("+") == true)
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer fixture-token")
        XCTAssertFalse(request.url!.absoluteString.contains("fixture-token"))
    }
    func testRejectsUnsafeConnectionURLs() {
        for value in ["http://files.example.com", "https://user:pass@files.example.com", "https://files.example.com?auth=secret", "https://files.example.com/#x", "files.example.com"] {
            XCTAssertThrowsError(try QuantumClient.validateURL(value))
        }
    }
    func testDistinguishesConflictAuthenticationAndPartialMove() throws {
        let url = URL(string: "https://files.example.com")!
        let client = QuantumClient(connection: Connection(name: "Test", url: url), token: "fixture")
        defer { client.close() }
        for code in [401, 403, 404, 409, 412, 207, 500] {
            XCTAssertThrowsError(try client.check(HTTPURLResponse(url: url, statusCode: code, httpVersion: nil, headerFields: nil)!))
        }
        XCTAssertNoThrow(try client.check(HTTPURLResponse(url: url, statusCode: 206, httpVersion: nil, headerFields: nil)!))
    }
    func testQuantumV2FixtureAndLeastPrivilegeDefaults() throws {
        let data = Data(#"{"scopes":[{"name":"Documents","scope":"/personal","permissions":{"view":true,"download":true}}]}"#.utf8)
        let user = try JSONDecoder().decode(User.self, from: data)
        XCTAssertTrue(user.scopes[0].permissions.download)
        XCTAssertFalse(user.scopes[0].permissions.modify)
        XCTAssertThrowsError(try JSONDecoder().decode(User.self, from: Data(#"{"sourcePermissions":{}}"#.utf8)))
        let listing = Data(#"{"name":"personal","path":"/personal","type":"directory","folders":[{"name":"旅行","path":"/personal/旅行","type":"directory"}],"files":[{"name":"clip.mp4","path":"/personal/clip.mp4","type":"video/mp4","size":42,"modified":"2026-01-01T12:00:00.123Z","hasPreview":true}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.children.count, 2)
        XCTAssertTrue(resource.children[0].isDirectory)
        XCTAssertTrue(resource.children[1].isMedia)
        XCTAssertNotNil(resource.children[1].date)
    }
    func testQuantumChildrenWithoutPaths() throws {
        // Quantum uses ItemInfo/ExtendedItemInfo for children, neither has a path.
        let listing = Data(#"{"name":"photos","path":"/photos/","type":"directory","folders":[{"name":"旅行","type":"directory"}],"files":[{"name":"a+b.mp4","type":"video/mp4","size":42,"viewToken":"view-fixture"}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.children.map(\.path), ["/photos/旅行", "/photos/a+b.mp4"])
        XCTAssertTrue(resource.children[0].isDirectory)
        XCTAssertEqual(resource.children[1].size, 42)
        XCTAssertEqual(resource.children[1].viewToken, "view-fixture")
        let root = Data(#"{"name":"/","path":"/","type":"directory","folders":[{"name":"docs","type":"directory"}],"files":null}"#.utf8)
        XCTAssertEqual(try JSONDecoder().decode(Resource.self, from: root).children[0].path, "/docs")
        let invalid = Data(#"{"name":"/","path":"/","type":"directory","files":[{"name":"../escape","type":"text/plain"}]}"#.utf8)
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
        let listing = Data(#"{"name":"photos","path":"/photos/","type":"directory","folders":[{"name":"2026","path":"/photos/2026/","type":"directory"}]}"#.utf8)
        let resource = try JSONDecoder().decode(Resource.self, from: listing)
        XCTAssertEqual(resource.file.path, "/photos")
        XCTAssertEqual(resource.children[0].path, try FilePath.child("2026", in: resource.file.path))
    }
}
