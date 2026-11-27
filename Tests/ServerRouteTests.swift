import XCTest
@testable import myFiles

final class ServerRouteTests: XCTestCase {
    func testLegacyConnectionDecodesWithoutLocalPairing() throws {
        let value = try JSONDecoder().decode(Connection.self, from: Data(#"{"id":"00000000-0000-0000-0000-000000000001","name":"Files","url":"https://files.example.com/companion"}"#.utf8))
        XCTAssertNil(value.localURL)
        XCTAssertNil(value.localFingerprint)
        XCTAssertNil(value.serverID)
    }
    func testRoutingPreservesCredentialsBodyAndEscapedQuery() throws {
        var request = URLRequest(url: URL(string: "https://files.example.com/companion/v1/jobs?path=a%2Bb%26c")!)
        request.httpMethod = "POST"
        request.httpBody = Data("body".utf8)
        request.setValue("Bearer fixture", forHTTPHeaderField: "Authorization")
        request.setValue("same-submission-key", forHTTPHeaderField: "Idempotency-Key")
        let routed = try ServerRoute.rebased(request, from: URL(string: "https://files.example.com/companion/")!, to: URL(string: "https://192.168.12.246:8095")!)
        XCTAssertEqual(routed.url?.absoluteString, "https://192.168.12.246:8095/v1/jobs?path=a%2Bb%26c")
        XCTAssertEqual(routed.httpBody, request.httpBody)
        XCTAssertEqual(routed.httpMethod, "POST")
        XCTAssertEqual(routed.value(forHTTPHeaderField: "Authorization"), "Bearer fixture")
        XCTAssertEqual(routed.value(forHTTPHeaderField: "Idempotency-Key"), "same-submission-key")
    }
    func testCannotRouteUnrelatedOriginOrPrefix() throws {
        let base = URL(string: "https://files.example.com/companion")!
        let local = URL(string: "https://192.168.12.246:8095")!
        for value in ["https://other.example.com/companion/v1/jobs", "https://files.example.com/companion-other/v1/jobs", "http://files.example.com/companion/v1/jobs"] {
            XCTAssertThrowsError(try ServerRoute.rebased(URLRequest(url: URL(string: value)!), from: base, to: local))
        }
    }
    func testUnpairedConnectionDoesNotProbeLocalNetwork() async throws {
        let connection = Connection(name: "Files", url: URL(string: "https://files.example.com")!)
        let request = URLRequest(url: connection.url.appendingPathComponent("v1/capabilities"))
        let routed = try await ServerRoute.shared.prepare(request, connection: connection)
        XCTAssertEqual(routed.url, request.url)
    }
}
