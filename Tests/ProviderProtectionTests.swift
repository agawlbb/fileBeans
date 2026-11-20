import XCTest
@testable import myFiles

final class ProviderProtectionTests: XCTestCase {
    func testProviderAllowsOnlyUnlockedProtectedRootsAndChildren() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = SharedStore(directory: directory)
        let connection = Connection(name: "Test", url: URL(string: "https://example.invalid")!)
        let provider = try Repository(connection: connection, store: store, token: "test", fileProvider: true)
        let app = try Repository(connection: connection, store: store, token: "test")
        let files = [RemoteFile(name: "Private", path: "/Private", type: "directory"),
                     RemoteFile(name: "Public", path: "/Private-copy", type: "directory")]
        let roots = try store.reconcile(connection: connection.id, parent: "source", source: "disk",
                                        files: files, permissions: Permissions(view: true, download: true))
        let privateRoot = try XCTUnwrap(roots.first { $0.file.name == "Private" })
        let publicRoot = try XCTUnwrap(roots.first { $0.file.name == "Public" })
        let child = try store.reconcile(connection: connection.id, parent: privateRoot.id, source: "disk",
            files: [RemoteFile(name: "photo.jpg", path: "/Private/photo.jpg", type: "image/jpeg")],
            permissions: Permissions(view: true, download: true))[0]
        for state in ["locked", "unlocked", "unknown", "error"] {
            try store.setVaults([ProtectedVault(id: "vault", source: "disk", state: state,
                initialized: true, path: "/Private")], connection: connection.id)
            for item in [privateRoot, child] {
                if state == "unlocked" {
                    XCTAssertNotNil(try provider.item(item.id).protection)
                    XCTAssertTrue(ProviderItem(try provider.item(item.id)).capabilities.contains(.allowsReading))
                } else {
                    XCTAssertThrowsError(try provider.item(item.id))
                    XCTAssertTrue(ProviderItem(try app.item(item.id)).capabilities.isEmpty)
                }
                XCTAssertNotNil(try app.item(item.id).protection)
            }
            XCTAssertNil(try provider.item(publicRoot.id).protection)
        }
    }

    func testScopedSourceInsideProtectedFolderFollowsUnlockState() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = SharedStore(directory: directory)
        let connection = Connection(name: "Test", url: URL(string: "https://example.invalid")!)
        try store.updateScope(connection: connection.id, source: "disk", boundary: "/Private/Subfolder")
        let root = try store.reconcile(connection: connection.id, parent: "root", source: "disk",
            files: [RemoteFile(name: "disk", path: "/", type: "directory")],
            permissions: Permissions(view: true), sourceRoots: true)[0]
        try store.setVaults([ProtectedVault(id: "vault", source: "disk", state: "unlocked",
            initialized: true, path: "/Private")], connection: connection.id)
        let provider = try Repository(connection: connection, store: store, token: "test", fileProvider: true)
        XCTAssertEqual(try provider.item(root.id).protection?.unlocked, true)
        try store.markProtectionLocked(connection: connection.id)
        XCTAssertThrowsError(try provider.item(root.id))
    }
}
