import XCTest
import FileProvider
@testable import myFiles

final class ProviderItemTests: XCTestCase {
    func testRemoteConflictUsesPortableErrorAndExplainsRecovery() {
        let error = providerError(APIError.conflict) as NSError
        XCTAssertEqual(error.domain, NSCocoaErrorDomain)
        XCTAssertEqual(error.code, CocoaError.Code.fileWriteUnknown.rawValue)
        XCTAssertEqual(error.localizedDescription, APIError.conflict.localizedDescription)
    }
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

extension ProviderItemTests {
    func testArchiveCapabilitiesAndUnlinkMetadata() {
        var stored = StoredItem(id: "archive", parent: "source", source: "Samba",
            file: RemoteFile(name: "pics", path: "/pics", type: "directory", archiveRole: "archive",
                             archiveLibrary: ArchiveEndpoint(source: "VPS", path: "/pics", available: true)),
            permissions: Permissions(view: true, download: true, modify: true, create: true, delete: true))
        let item = ProviderItem(stored)
        XCTAssertTrue(item.capabilities.contains(.allowsReading))
        XCTAssertTrue(item.capabilities.contains(.allowsContentEnumerating))
        let mutations: [NSFileProviderItemCapabilities] = [.allowsAddingSubItems, .allowsWriting, .allowsDeleting, .allowsRenaming, .allowsReparenting]
        for capability in mutations {
            XCTAssertFalse(item.capabilities.contains(capability))
        }
        XCTAssertThrowsError(try stored.file.requireArchiveWritable()) { error in
            XCTAssertEqual(error.localizedDescription, "Make changes in VPS /pics.")
        }
        let before = item.itemVersion
        stored.file.archiveRole = nil; stored.file.archiveLibrary = nil
        XCTAssertNoThrow(try stored.file.requireArchiveWritable())
        XCTAssertTrue(ProviderItem(stored).capabilities.contains(.allowsAddingSubItems))
        XCTAssertNotEqual(before.metadataVersion, ProviderItem(stored).itemVersion.metadataVersion)
        XCTAssertEqual(before.contentVersion, ProviderItem(stored).itemVersion.contentVersion)
    }
    func testArchiveErrorHidesInternalPeerCoordinatesAndUnknownPaths() {
        var file = RemoteFile(name: "photo.jpg", path: "/pics/photo.jpg", type: "image/jpeg", archiveRole: "archive",
                              archiveLibrary: ArchiveEndpoint(source: "peer:filebeans-vps:VPS", path: "/pics", available: true))
        XCTAssertEqual(file.archiveWriteMessage, "Make changes in VPS /pics.")
        file.archiveLibrary = ArchiveEndpoint(source: "peer:filebeans-vps:VPS", path: ".", available: false)
        XCTAssertEqual(file.archiveWriteMessage, "Make changes in the linked folder on VPS.")
    }

}
