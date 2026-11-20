import XCTest
@testable import myFiles

final class NormalizationTests: XCTestCase {
    func testOldCompanionDoesNotAdvertiseEnabledNormalization() throws {
        let data = Data(#"{"version":1,"roots":[],"driveConnected":true}"#.utf8)
        XCTAssertNil(try JSONDecoder().decode(CompanionCapabilities.self, from: data).normalizationEnabled)
    }
}
