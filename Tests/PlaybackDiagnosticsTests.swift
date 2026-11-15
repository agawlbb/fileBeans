import XCTest
@testable import myFiles

final class PlaybackDiagnosticsTests: XCTestCase {
    func testUnderlyingTransportErrorSurvivesWithoutPrivateContext() {
        let privateURL = "https://private.example/stream?viewToken=private-grant"
        let underlying = NSError(domain: NSURLErrorDomain, code: -1011, userInfo: [
            NSLocalizedDescriptionKey: privateURL,
            NSURLErrorFailingURLErrorKey: URL(string: privateURL)!
        ])
        let error = NSError(domain: "AVFoundationErrorDomain", code: -11850, userInfo: [
            NSUnderlyingErrorKey: underlying,
            NSLocalizedDescriptionKey: "Authorization: Bearer private-token"
        ])
        XCTAssertEqual(PlaybackDiagnostics.summary(error: error, mediaStatusCodes: [403]),
                       "AVFoundation -11850; URL -1011; Media status 403")
    }

    func testUnknownDomainIsNotExposed() {
        let error = NSError(domain: "https://private.example/?token=secret", code: 42)
        XCTAssertEqual(PlaybackDiagnostics.summary(error: error), "Player 42")
    }

    func testMissingErrorAndRecentMediaStatuses() {
        XCTAssertEqual(PlaybackDiagnostics.summary(error: nil), "No diagnostic code was provided by the player.")
        XCTAssertEqual(PlaybackDiagnostics.summary(error: nil, mediaStatusCodes: [500, 401, 0, -12939, -12939]),
                       "Media status 401; Media status -12939")
    }

    func testLongErrorChainIsBounded() {
        var error = NSError(domain: NSOSStatusErrorDomain, code: -1)
        for code in 1...20 {
            error = NSError(domain: "AVFoundationErrorDomain", code: code, userInfo: [NSUnderlyingErrorKey: error])
        }
        let summary = PlaybackDiagnostics.summary(error: error)
        XCTAssertEqual(summary.components(separatedBy: "; ").count, 8)
        XCTAssertTrue(summary.hasSuffix("AVFoundation 13"))
    }
}
