import XCTest
import FileProvider
@testable import myFiles

final class ProviderItemTests: XCTestCase {
    func testReadOnlyFilesDoNotExposeMutations() {
        let stored = StoredItem(id: "file", parent: "folder", source: "disk",
            file: RemoteFile(name: "photo.jpg", path: "/photo.jpg", type: "image/jpeg", size: 42),
            permissions: Permissions(view: true, download: true))
        let item = ProviderItem(stored)
        XCTAssertTrue(item.capabilities.contains(.allowsReading))
        XCTAssertFalse(item.capabilities.contains(.allowsWriting))
        XCTAssertFalse(item.capabilities.contains(.allowsDeleting))
        XCTAssertFalse(item.capabilities.contains(.allowsRenaming))
        XCTAssertEqual(item.contentType.identifier, "public.jpeg")
    }
    func testSourceRootSupportsCreationButCannotBeMovedOrDeleted() {
        let stored = StoredItem(id: "source", parent: "root", source: "disk",
            file: RemoteFile(name: "disk", path: "/", type: "directory"),
            permissions: Permissions(view: true, download: true, modify: true, create: true, delete: true), isSource: true)
        let item = ProviderItem(stored)
        XCTAssertEqual(item.parentItemIdentifier, .rootContainer)
        XCTAssertTrue(item.capabilities.contains(.allowsContentEnumerating))
        XCTAssertTrue(item.capabilities.contains(.allowsAddingSubItems))
        XCTAssertFalse(item.capabilities.contains(.allowsDeleting))
        XCTAssertFalse(item.capabilities.contains(.allowsReparenting))
    }
    func testPermissionChangesUpdateMetadataVersionWithoutChangingContentVersion() {
        var stored = StoredItem(id: "file", parent: "folder", source: "disk",
            file: RemoteFile(name: "document.txt", path: "/document.txt", type: "text/plain", size: 42),
            permissions: Permissions(view: true, download: true, modify: true))
        let before = ProviderItem(stored).itemVersion
        stored.permissions.modify = false
        let after = ProviderItem(stored).itemVersion
        XCTAssertEqual(before.contentVersion, after.contentVersion)
        XCTAssertNotEqual(before.metadataVersion, after.metadataVersion)
    }
}
