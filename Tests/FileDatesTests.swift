import XCTest
@testable import myFiles

final class FileDatesTests: XCTestCase {
    func testFractionalAndStandardTimestampsAndInvalidInput() {
        let fractional = FileDates.parse("2026-11-20T09:44:18.663Z")
        let standard = FileDates.parse("2026-11-20T09:44:18Z")
        XCTAssertNotNil(fractional); XCTAssertNotNil(standard)
        XCTAssertEqual(fractional!.timeIntervalSince(standard!), 0.663, accuracy: 0.001)
        XCTAssertEqual(FileDates.parse("2026-11-20T09:44:18.663Z"), fractional)
        XCTAssertNil(FileDates.parse("invalid"))
    }
    func testRepeatedLargeFolderTimestampParsing() {
        let files = (0..<10000).map { RemoteFile(name: "\($0).jpg", path: "/\($0).jpg", type: "image/jpeg", modified: "2026-11-20T09:44:18.663Z") }
        for _ in 0..<3 { XCTAssertEqual(files.compactMap(\.date).count, 10000) }
    }
}
