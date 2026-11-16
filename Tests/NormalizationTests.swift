import XCTest
@testable import myFiles

final class NormalizationTests: XCTestCase {
    func testDownloadAndEncodingProgressUseDistinctUnits() throws {
        let json = #"{"id":"job","request":{"source":"drive","path":"file","relativePath":"vids/movie.mov"},"state":"running","phase":"downloading","sourceSize":2000000,"outputSize":0,"done":1000000,"total":2000000}"#
        let downloading = try JSONDecoder().decode(NormalizationJob.self, from: Data(json.utf8))
        XCTAssertEqual(downloading.phaseTitle, "Downloading from Drive")
        XCTAssertEqual(downloading.title, "vids/movie.mov")
        XCTAssertTrue(downloading.active)
        let encoding = try JSONDecoder().decode(NormalizationJob.self, from: Data(json.replacingOccurrences(of: "downloading", with: "converting").utf8))
        XCTAssertEqual(encoding.phaseTitle, "Optimizing media")
    }
    func testFailuresRemainVisibleAndDoNotCountAsActive() throws {
        let json = #"{"id":"job","request":{"source":"VPS","path":"pics/movie.mp4"},"state":"failed","phase":"validating","sourceSize":200,"outputSize":100,"done":100,"total":100,"error":"Source changed"}"#
        let job = try JSONDecoder().decode(NormalizationJob.self, from: Data(json.utf8))
        XCTAssertFalse(job.active)
        XCTAssertEqual(job.error, "Source changed")
    }
    func testOldCompanionDoesNotAdvertiseEnabledNormalization() throws {
        let data = Data(#"{"version":1,"roots":[],"driveConnected":true}"#.utf8)
        XCTAssertNil(try JSONDecoder().decode(CompanionCapabilities.self, from: data).normalizationEnabled)
    }
}
