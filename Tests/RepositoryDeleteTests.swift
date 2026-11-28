import XCTest
@testable import myFiles

private final class DeleteAuthorityProtocol: URLProtocol {
    static var reject = false
    static var posts = 0
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let path = request.url!.path
        let status: Int
        let body: String
        if path.hasSuffix("/trash"), request.httpMethod == "POST" {
            Self.posts += 1
            status = Self.reject ? 403 : 200
            body = Self.reject ? #"{"error":"Make changes in VPS /pics."}"# : "{}"
        } else if path.hasSuffix("/vaults") {
            status = 200; body = "[]"
        } else if path.hasSuffix("/resources") {
            status = 200
            body = #"{"name":"vps_exchange","path":"/vps_exchange","type":"directory","children":[]}"#
        } else {
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL)); return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

final class RepositoryDeleteTests: XCTestCase {
    func testStaleArchiveFlagCannotBlockAnOrdinaryServerDelete() async throws {
        try await checkDelete(rejected: false)
    }
    func testServerArchiveRejectionStillPreventsDelete() async throws {
        try await checkDelete(rejected: true)
    }
    private func checkDelete(rejected: Bool) async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = SharedStore(directory: directory)
        let connection = Connection(name: "Pi", url: URL(string: "https://pi.example/companion")!)
        let permissions = Permissions(view: true, download: true, modify: true, create: true, delete: true)
        let parent = try store.reconcile(connection: connection.id, parent: "root", source: "Samba",
            files: [RemoteFile(name: "vps_exchange", path: "/vps_exchange", type: "directory")], permissions: permissions)[0]
        let item = try store.reconcile(connection: connection.id, parent: parent.id, source: "Samba",
            files: [RemoteFile(name: "ordinary.txt", path: "/vps_exchange/ordinary.txt", type: "text/plain", archiveRole: "archive",
                               archiveLibrary: ArchiveEndpoint(source: "peer:filebeans-vps:VPS", path: ".", available: false))], permissions: permissions)[0]
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [DeleteAuthorityProtocol.self]
        DeleteAuthorityProtocol.reject = rejected; DeleteAuthorityProtocol.posts = 0
        let repository = try Repository(connection: connection, store: store, token: "synthetic", configuration: configuration)
        if rejected {
            do { try await repository.delete(item); XCTFail("Server archive guard was ignored") }
            catch { XCTAssertEqual(error.localizedDescription, "Make changes in VPS /pics.") }
            XCTAssertNotNil(try store.items(connection.id)[item.id])
        } else {
            try await repository.delete(item)
            XCTAssertNil(try store.items(connection.id)[item.id])
        }
        XCTAssertEqual(DeleteAuthorityProtocol.posts, 1)
    }
}
