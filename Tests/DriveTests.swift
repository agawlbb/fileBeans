import XCTest
@testable import myFiles

final class DriveTests: XCTestCase {
    func testFolderOrderingIsConsistentAcrossSources() {
        let folder = BrowserSortValue(id: "folder", name: "Z folder", folder: true, size: 0, modified: .distantPast)
        let file = BrowserSortValue(id: "file", name: "A video", folder: false, size: 100, modified: .distantFuture)
        for order in ["name", "size", "modified"] {
            XCTAssertTrue(BrowserOrdering.precedes(folder, file, order: order))
            XCTAssertFalse(BrowserOrdering.precedes(file, folder, order: order))
        }
    }
    func testEmptyDriveCollectionsDecodeWhenGoogleOmitsArrays() throws {
        for json in ["{}", #"{"files":null,"drives":null}"#] {
            let data = Data(json.utf8)
            XCTAssertTrue(try JSONDecoder().decode(DrivePage.self, from: data).files.isEmpty)
            XCTAssertTrue(try JSONDecoder().decode(SharedDrivePage.self, from: data).drives.isEmpty)
        }
    }
    func testDrivePermissionsAndNativeDocuments() throws {
        let native = try JSONDecoder().decode(DriveFile.self, from: Data(#"{"id":"doc","name":"Document","mimeType":"application/vnd.google-apps.document","capabilities":{"canCopy":true,"canRename":false,"canDownload":true,"canTrash":true}}"#.utf8))
        XCTAssertTrue(native.nativeDocument)
        XCTAssertTrue(native.canCopy)
        XCTAssertTrue(native.canTrash)
        XCTAssertFalse(native.canRename)
        XCTAssertFalse(native.downloadable)
        let sharedRoot = DriveFile(id: "shared", name: "Shared", mimeType: "application/vnd.google-apps.folder", sourceRoot: true)
        XCTAssertTrue(sharedRoot.folder)
        XCTAssertFalse(sharedRoot.canCopy)
        XCTAssertFalse(sharedRoot.canMove)
    }
    func testDriveModificationDatesIncludeFractionalSeconds() {
        let file = DriveFile(id: "file", name: "file", mimeType: "image/jpeg", modifiedTime: "2026-11-16T10:11:12.123Z")
        XCTAssertGreaterThan(file.sortValue.modified.timeIntervalSince1970, 1_790_000_000)
    }
    func testOldCompanionCapabilitiesRemainReadable() throws {
        let capabilities = try JSONDecoder().decode(CompanionCapabilities.self, from: Data(#"{"version":1,"roots":[{"source":"VPS","trash":"/Trash"}],"driveConnected":true}"#.utf8))
        XCTAssertTrue(capabilities.driveConnected)
        XCTAssertNil(capabilities.driveWriteAccess)
        XCTAssertNil(capabilities.features)
    }
    func testDestinationModeRestrictsMovesToOneProvider() {
        XCTAssertTrue(DestinationMode.copy.allowsDrive)
        XCTAssertTrue(DestinationMode.copy.allowsLocal)
        XCTAssertFalse(DestinationMode.localMove("VPS").allowsDrive)
        XCTAssertFalse(DestinationMode.driveMove.allowsLocal)
    }
}
