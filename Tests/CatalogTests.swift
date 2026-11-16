import XCTest
@testable import myFiles

final class CatalogTests: XCTestCase {
    private var directory: URL!
    private var store: SharedStore!
    private let connection = UUID()
    private let permissions = Permissions(view: true, download: true, modify: true, create: true, delete: true)
    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = SharedStore(directory: directory)
    }
    override func tearDownWithError() throws { try FileManager.default.removeItem(at: directory) }
    private func folder(_ name: String, _ path: String) -> RemoteFile {
        RemoteFile(name: name, path: path, type: "directory")
    }
    func testConcurrentStoreInstancesDoNotLoseUpdates() throws {
        let directory = try XCTUnwrap(directory)
        let errorLock = NSLock()
        var errors: [Error] = []
        DispatchQueue.concurrentPerform(iterations: 24) { index in
            do {
                try SharedStore(directory: directory).save(Connection(
                    name: "Server \(index)", url: URL(string: "https://example.com")!))
            } catch {
                errorLock.lock()
                errors.append(error)
                errorLock.unlock()
            }
        }
        XCTAssertTrue(errors.isEmpty, "\(errors)")
        XCTAssertEqual(try store.connections().count, 24)
    }
    func testThrowingTransactionReleasesCoordinationAndDoesNotPersistChanges() throws {
        enum Failure: Error { case expected }
        try store.save(Connection(name: "Original", url: URL(string: "https://example.com")!))
        XCTAssertThrowsError(try store.transaction { state in
            state.connections.removeAll()
            throw Failure.expected
        })
        let reopened = SharedStore(directory: directory)
        XCTAssertEqual(try reopened.connections().map(\.name), ["Original"])
        try reopened.save(Connection(name: "Next", url: URL(string: "https://example.com")!))
        XCTAssertEqual(try store.connections().count, 2)
    }
    func testIdentitySurvivesRenameIncludingDescendantsAndRelaunch() throws {
        let roots = try store.reconcile(connection: connection, parent: "root", source: "disk",
            files: [folder("disk", "/")], permissions: permissions, sourceRoots: true)
        let root = roots[0]
        let parent = try store.reconcile(connection: connection, parent: root.id, source: "disk",
            files: [folder("before", "/before")], permissions: permissions)[0]
        let child = try store.reconcile(connection: connection, parent: parent.id, source: "disk",
            files: [folder("nested", "/before/nested")], permissions: permissions)[0]
        try store.relocate(parent, connection: connection, path: "/after", parent: root.id)
        let refreshed = try store.reconcile(connection: connection, parent: root.id, source: "disk",
            files: [folder("after", "/after")], permissions: permissions)
        XCTAssertEqual(refreshed[0].id, parent.id)
        let reopened = SharedStore(directory: directory)
        XCTAssertEqual(try reopened.items(connection)[child.id]?.file.path, "/after/nested")
        XCTAssertEqual(try reopened.items(connection)[child.id]?.parent, parent.id)
    }
    func testDeletionPersistsTombstonesAndPrunesDescendants() throws {
        let parent = try store.reconcile(connection: connection, parent: "source", source: "disk",
            files: [folder("gone", "/gone")], permissions: permissions)[0]
        let child = try store.reconcile(connection: connection, parent: parent.id, source: "disk",
            files: [folder("nested", "/gone/nested")], permissions: permissions)[0]
        _ = try store.reconcile(connection: connection, parent: "source", source: "disk", files: [], permissions: permissions)
        XCTAssertNil(try store.items(connection)[parent.id])
        XCTAssertNil(try store.items(connection)[child.id])
        let tombstone = try store.transaction { $0.deleted[connection.uuidString]?.values.first { $0.id == parent.id } }
        XCTAssertEqual(tombstone?.parent, "source")
        XCTAssertGreaterThan(tombstone?.revision ?? 0, 0)
    }
    func testSourcesDoNotShareIdentitiesAndRevokedPermissionsPropagate() throws {
        let a = try store.reconcile(connection: connection, parent: "root", source: "a",
            files: [folder("a", "/")], permissions: permissions, sourceRoots: true)[0]
        let b = try store.reconcile(connection: connection, parent: "root", source: "b",
            files: [folder("b", "/")], permissions: permissions, sourceRoots: true)[0]
        XCTAssertNotEqual(a.id, b.id)
        let child = try store.reconcile(connection: connection, parent: a.id, source: "a",
            files: [folder("child", "/child")], permissions: permissions)[0]
        _ = try store.reconcile(connection: connection, parent: "root", source: "a",
            files: [folder("a", "/")], permissions: Permissions(view: true), sourceRoots: true)
        XCTAssertFalse(try XCTUnwrap(store.items(connection)[child.id]).permissions.modify)
        XCTAssertTrue(try XCTUnwrap(store.items(connection)[b.id]).permissions.modify)
    }
    func testCorruptCatalogIsNotSilentlyOverwritten() throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let url = directory.appendingPathComponent("catalog.json")
        try Data("invalid".utf8).write(to: url)
        XCTAssertThrowsError(try store.connections())
        XCTAssertEqual(try String(contentsOf: url, encoding: .utf8), "invalid")
    }
    func testNarrowingSourceScopeKeepsNewRootAndRemovesOldDescendants() throws {
        let root = try store.reconcile(connection: connection, parent: "root", source: "disk",
            files: [folder("disk", "/")], permissions: permissions, sourceRoots: true)[0]
        let old = try store.reconcile(connection: connection, parent: root.id, source: "disk",
            files: [folder("private", "/private")], permissions: permissions)[0]
        let replacement = try store.reconcile(connection: connection, parent: "root", source: "disk",
            files: [folder("disk", "/limited")], permissions: permissions, sourceRoots: true)[0]
        XCTAssertNotNil(try store.items(connection)[replacement.id])
        XCTAssertNil(try store.items(connection)[root.id])
        XCTAssertNil(try store.items(connection)[old.id])
    }
    func testChangedServerScopeInvalidatesCachedRelativePaths() throws {
        try store.updateScope(connection: connection, source: "disk", boundary: "/users/alice")
        let root = try store.reconcile(connection: connection, parent: "root", source: "disk",
            files: [folder("disk", "/")], permissions: permissions, sourceRoots: true)[0]
        _ = try store.reconcile(connection: connection, parent: root.id, source: "disk",
            files: [folder("documents", "/documents")], permissions: permissions)
        try store.updateScope(connection: connection, source: "disk", boundary: "/users/other")
        XCTAssertTrue(try store.items(connection).isEmpty)
        let recreated = try store.reconcile(connection: connection, parent: "root", source: "disk",
            files: [folder("disk", "/")], permissions: permissions, sourceRoots: true)[0]
        XCTAssertNotEqual(recreated.id, root.id)
    }
}
