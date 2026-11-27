import XCTest
@testable import myFiles

final class IndependentSourcesTests: XCTestCase {
    func testLegacySavedConnectionRemainsOneServer() throws {
        let data = Data(#"{"id":"C1CA57AC-672A-45D0-9908-76E73118B133","name":"VPS","url":"https://files.example/companion"}"#.utf8)
        let connection = try JSONDecoder().decode(Connection.self, from: data)
        XCTAssertFalse(connection.isGoogleDrive)
        XCTAssertEqual(connection.unified.companionURL, connection.url)
        XCTAssertEqual(connection.unified.id, connection.id)
    }
    func testGoogleRequestsNeverUseAnInheritedFileBeansAddress() throws {
        var account = Connection(name: "Drive", url: URL(string: "https://www.googleapis.com/drive/v3")!)
        account.kind = .googleDrive
        account.companionURL = URL(string: "https://unrelated.example/companion")!
        account.localURL = URL(string: "https://192.168.1.5:8095")!
        let transport = GoogleDriveTransport(connection: account)
        defer { transport.close() }
        let address = try transport.url("files/document", query: ["alt": "media"])
        XCTAssertEqual(address.host, "www.googleapis.com")
        XCTAssertEqual(address.path, "/drive/v3/files/document")
        XCTAssertEqual(URLComponents(url: address, resolvingAgainstBaseURL: false)?.queryItems?.first?.value, "media")
    }
    func testGoogleAccountCannotBecomeAFileProviderRepository() throws {
        var account = Connection(name: "Drive", url: URL(string: "https://www.googleapis.com/drive/v3")!)
        account.kind = .googleDrive
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        XCTAssertThrowsError(try Repository(connection: account, store: SharedStore(directory: directory)))
    }
    func testGoogleCredentialsCannotFollowExternalOrPlaintextURLs() {
        for value in ["http://www.googleapis.com/upload", "https://www.googleapis.com.attacker.example/upload",
                      "https://user:secret@www.googleapis.com/upload", "https://www.googleapis.com:8443/upload"] {
            XCTAssertFalse(GoogleDriveTransport.googleURL(URL(string: value)!))
        }
        XCTAssertTrue(GoogleDriveTransport.googleURL(URL(string: "https://www.googleapis.com/upload/drive/v3/files")!))
        XCTAssertThrowsError(try GoogleDriveTransport.id("../foreign/path"))
    }
}
