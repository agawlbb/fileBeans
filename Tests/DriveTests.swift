import XCTest
@testable import myFiles

final class DriveTests: XCTestCase {
    func testFolderOrderingIsConsistentAcrossSources() {
        let folder = BrowserSortValue(id: "folder", name: "Z folder", folder: true, size: 0, modified: .distantPast)
        let file = BrowserSortValue(id: "file", name: "A video", folder: false, size: 100, modified: .distantFuture)
        for order in ["name", "size", "modified", "modifiedAscending"] {
            XCTAssertTrue(BrowserOrdering.precedes(folder, file, order: order))
            XCTAssertFalse(BrowserOrdering.precedes(file, folder, order: order))
        }
    }
    func testModifiedOrderingAndToggle() {
        func item(_ id: String, _ date: Date) -> BrowserSortValue {
            BrowserSortValue(id: id, name: id, folder: false, size: 0, modified: date)
        }
        let older = item("A", Date(timeIntervalSince1970: 1))
        let newer = item("B", Date(timeIntervalSince1970: 2))
        let missing = item("C", .distantPast)
        XCTAssertEqual(BrowserOrdering.selectingModified("name"), "modified")
        XCTAssertEqual(BrowserOrdering.selectingModified("modified"), "modifiedAscending")
        XCTAssertEqual(BrowserOrdering.selectingModified("modifiedAscending"), "modified")
        XCTAssertTrue(BrowserOrdering.precedes(newer, older, order: "modified"))
        XCTAssertTrue(BrowserOrdering.precedes(older, newer, order: "modifiedAscending"))
        for order in ["modified", "modifiedAscending"] {
            XCTAssertTrue(BrowserOrdering.precedes(older, missing, order: order))
            XCTAssertFalse(BrowserOrdering.precedes(missing, older, order: order))
            XCTAssertTrue(BrowserOrdering.precedes(older, item("B", older.modified), order: order))
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
    func testDestinationModeAllowsCrossDriveMovesAndRetainsLegacySourceRestriction() {
        XCTAssertTrue(DestinationMode.copy.allowsDrive)
        XCTAssertTrue(DestinationMode.copy.allowsLocal)
        XCTAssertFalse(DestinationMode.localMove("VPS").allowsDrive)
        XCTAssertTrue(DestinationMode.localMove(nil).allowsDrive)
        XCTAssertTrue(DestinationMode.driveMove.allowsLocal)
    }
}
