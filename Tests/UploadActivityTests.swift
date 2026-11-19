import XCTest
@testable import myFiles

@MainActor
final class UploadActivityTests: XCTestCase {
    func testUploadsAreScopedAndKeepIdentityThroughServerHandoff() {
        let activity = UploadActivity(), connection = UUID(), id = UUID()
        let upload = UploadTransfer(id: id, connection: connection, name: "photo.jpg", destination: "Drive", total: 100)
        activity.add(upload); activity.add(upload)
        XCTAssertEqual(activity.transfers(connection: connection).count, 1)
        XCTAssertTrue(activity.transfers(connection: UUID()).isEmpty)
        activity.update(id, progress: 0.5, state: "Uploading")
        XCTAssertEqual(activity.entries.first?.progress, 0.5)
        activity.update(id, progress: 0, state: "Copying to Google Drive", job: "server-job")
        XCTAssertEqual(activity.entries.first?.job, "server-job")
        XCTAssertEqual(activity.entries.first?.id, id)
        activity.remove(id)
        XCTAssertTrue(activity.entries.isEmpty)
    }

}
