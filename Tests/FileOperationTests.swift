import XCTest
@testable import myFiles

@MainActor
final class FileOperationTests: XCTestCase {
    private func job(state: String = "running", name: String? = nil) throws -> CopyJob {
        var request: [String: Any] = ["items": [["source": "Samba", "path": "/Private/video.mov"]],
            "destination": ["source": "Samba", "path": "Documents/Target"], "operation": "move"]
        if let name { request["destinationName"] = name }
        let data = try JSONSerialization.data(withJSONObject: ["id": "transfer", "state": state,
            "total": 100, "done": 20, "speed": 1, "current": "", "request": request])
        return try JSONDecoder().decode(CopyJob.self, from: data)
    }
    func testDestinationAppearsBeforeSubmissionAndSurvivesJobAcknowledgement() throws {
        let activity = FileActivity(), connection = UUID()
        let ids = activity.begin([ActivityFile(source: "Samba", path: "/Private/video.mov", name: "video.mov", folder: false)],
            connection: connection, operation: "move", destination: CopyLocation(source: "Samba", path: "/Target"))
        XCTAssertEqual(activity.pending(connection: connection, source: "Samba", parent: "/Target", canonicalParent: "/Documents/Target").map(\.name), ["video.mov"])
        activity.attach(try job(), ids: ids, connection: connection)
        XCTAssertEqual(activity.pending(connection: connection, source: "Samba", parent: "/Target", canonicalParent: "/Documents/Target").count, 1)
        activity.synchronize([try job(state: "completed")], connection: connection)
        XCTAssertTrue(activity.pending(connection: connection, source: "Samba", parent: "/Target", canonicalParent: "/Documents/Target").isEmpty)
    }
    func testServerJobRebuildsDestinationAfterAppRestartAndRespectsScopeAndRename() throws {
        let activity = FileActivity(), connection = UUID()
        activity.synchronize([try job(name: "renamed.mov")], connection: connection)
        let pending = activity.pending(connection: connection, source: "Samba", parent: "/Target", canonicalParent: "/Documents/Target")
        XCTAssertEqual(pending.map(\.name), ["renamed.mov"])
        XCTAssertEqual(pending.map(\.path), ["/Target/renamed.mov"])
        XCTAssertTrue(activity.pending(connection: connection, source: "VPS", parent: "/Target", canonicalParent: "/Documents/Target").isEmpty)
    }

    func testServerJobsOutsideAuthenticatedScopeDoNotAppearInSameNamedFolder() throws {
        let activity = FileActivity(), connection = UUID()
        activity.synchronize([try job()], connection: connection)
        XCTAssertTrue(activity.pending(connection: connection, source: "Samba", parent: "/Documents/Target", canonicalParent: "/Other/Documents/Target").isEmpty)
    }

    func testReopenedSourceRecognizesQueuedBatchMove() throws {
        let activity = FileActivity(), connection = UUID()
        activity.synchronize([try job()], connection: connection)
        XCTAssertTrue(activity.busySource(connection: connection, source: "Samba", path: "/video.mov", canonicalPath: "/Private/video.mov"))
        XCTAssertFalse(activity.busySource(connection: connection, source: "Samba", path: "/other.mov", canonicalPath: "/Private/other.mov"))
    }
    func testEntireDeleteSelectionIsPendingImmediatelyAndFailuresClearIndividually() {
        let activity = FileActivity(), connection = UUID()
        let ids = activity.begin(["a", "b", "c"].map { ActivityFile(source: "Samba", path: "/" + $0, name: $0, folder: false) },
            connection: connection, operation: "delete")
        for name in ["a", "b", "c"] { XCTAssertTrue(activity.deleting(connection: connection, source: "Samba", path: "/" + name)) }
        activity.finish([ids[1]])
        XCTAssertFalse(activity.deleting(connection: connection, source: "Samba", path: "/b"))
        XCTAssertTrue(activity.deleting(connection: connection, source: "Samba", path: "/c"))
        activity.finish([ids[0], ids[2]])
        XCTAssertTrue(activity.entries.isEmpty)
    }
    func testFailedTransferClearsDestinationAndSurfacesError() throws {
        let activity = FileActivity(), connection = UUID()
        let ids = activity.begin([ActivityFile(source: "Samba", path: "/x", name: "x", folder: true)],
            connection: connection, operation: "copy", destination: CopyLocation(source: "Samba", path: "/Target"))
        activity.attach(try job(), ids: ids, connection: connection)
        activity.synchronize([try job(state: "failed")], connection: connection)
        XCTAssertTrue(activity.entries.isEmpty)
        XCTAssertNotNil(activity.errors[connection])
        XCTAssertTrue(activity.pending(connection: connection, source: "Samba", parent: "/Target", canonicalParent: "/Documents/Target").isEmpty)
    }

    func testOldFailuresStayInHistoryWhileSuccessfulCopyIsVisibleThere() throws {
        let activity = FileActivity(), connection = UUID()
        let incoming = [try job(state: "failed")]
        activity.synchronize(incoming, connection: connection)
        XCTAssertNil(activity.errors[connection])
        XCTAssertTrue(activity.visibleTransfers(incoming, connection: connection, history: false).isEmpty)
        XCTAssertEqual(activity.visibleTransfers(incoming, connection: connection, history: true).count, 1)
        let completed = [try job(state: "completed")]
        XCTAssertEqual(activity.visibleTransfers(completed, connection: connection, history: true).first?.state, "completed")
    }

    func testNewFailureExpiresWithoutBeingRenewedByPolling() throws {
        let activity = FileActivity(), connection = UUID(), now = Date()
        activity.synchronize([try job()], connection: connection, now: now)
        let failed = [try job(state: "failed")]
        activity.synchronize(failed, connection: connection, now: now)
        XCTAssertEqual(activity.visibleTransfers(failed, connection: connection, history: false, now: now).count, 1)
        activity.synchronize(failed, connection: connection, now: now.addingTimeInterval(29))
        activity.expireFailureNotices(now: now.addingTimeInterval(30))
        XCTAssertNil(activity.errors[connection])
        XCTAssertTrue(activity.visibleTransfers(failed, connection: connection, history: false, now: now.addingTimeInterval(30)).isEmpty)
        XCTAssertEqual(activity.visibleTransfers(failed, connection: connection, history: true).count, 1)
    }

    func testDismissedFailureDoesNotReturnOnRefresh() throws {
        let activity = FileActivity(), connection = UUID()
        activity.synchronize([try job()], connection: connection)
        let failed = [try job(state: "failed")]
        activity.synchronize(failed, connection: connection)
        activity.dismissFailure("transfer", connection: connection)
        activity.synchronize(failed, connection: connection)
        XCTAssertNil(activity.errors[connection])
        XCTAssertTrue(activity.visibleTransfers(failed, connection: connection, history: false).isEmpty)
    }

    func testRecoveredJobClearsItsFailureAlert() throws {
        let activity = FileActivity(), connection = UUID()
        activity.synchronize([try job()], connection: connection)
        activity.synchronize([try job(state: "failed")], connection: connection)
        XCTAssertNotNil(activity.errors[connection])
        activity.synchronize([try job(state: "completed")], connection: connection)
        XCTAssertNil(activity.errors[connection])
        XCTAssertTrue(activity.failureNotices[connection]?.isEmpty == true)
    }

    func testFailureExpiryPreservesOtherConnectionsAndNewerErrors() throws {
        let activity = FileActivity(), connection = UUID(), other = UUID(), now = Date()
        activity.synchronize([try job()], connection: connection, now: now)
        activity.synchronize([try job(state: "failed")], connection: connection, now: now)
        activity.errors[connection] = "Another operation failed"
        activity.errors[other] = "Connection unavailable"
        activity.expireFailureNotices(now: now.addingTimeInterval(30))
        XCTAssertEqual(activity.errors[connection], "Another operation failed")
        XCTAssertEqual(activity.errors[other], "Connection unavailable")
    }

    func testLegacyTrashCacheIsRemovedWithoutRemovingNormalFiles() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = SharedStore(directory: directory), connection = UUID()
        let permissions = Permissions(view: true, download: true)
        let files = [RemoteFile(name: "Trash", path: "/Private/Trash", type: "directory"),
                     RemoteFile(name: "real.mov", path: "/Private/real.mov", type: "video/quicktime")]
        let cached = try store.reconcile(connection: connection, parent: "private", source: "Samba", files: files, permissions: permissions)
        _ = try store.reconcile(connection: connection, parent: cached[0].id, source: "Samba",
            files: [RemoteFile(name: "old-id", path: "/Private/Trash/old-id", type: "directory")], permissions: permissions)
        try store.setVaults([ProtectedVault(id: "vault", source: "Samba", state: "unlocked", initialized: true, path: "/Private")], connection: connection)
        let current = try store.items(connection)
        XCTAssertNil(current[cached[0].id])
        XCTAssertEqual(current.values.map { $0.file.name }, ["real.mov"])
        let file = StoredItem(id: "normal", parent: "docs", source: "Samba", file: RemoteFile(name: "Trash", path: "/Trash", type: "directory"), permissions: permissions)
        XCTAssertFalse(SharedStore.isSystemEntry(file, vaults: [], scope: "/Documents"))
    }
    func testHiddenLockedCatalogEntriesKeepTheirIDsAcrossUnlock() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = SharedStore(directory: directory), connection = UUID()
        let permissions = Permissions(view: true, download: true, modify: true, create: true, delete: true)
        var vault = ProtectedVault(id: "vault", source: "Samba", state: "unlocked", initialized: true, path: "/Private")
        try store.setVaults([vault], connection: connection)
        let file = RemoteFile(name: "Private", path: "/Private", type: "directory")
        let original = try store.reconcile(connection: connection, parent: "source", source: "Samba", files: [file], permissions: permissions)[0]
        vault.state = "locked"; try store.setVaults([vault], connection: connection)
        let revision = try store.revision(connection)
        let hidden = try store.reconcile(connection: connection, parent: "source", source: "Samba", files: [], permissions: permissions)
        XCTAssertTrue(hidden.isEmpty)
        XCTAssertEqual(try store.items(connection)[original.id]?.protection?.unlocked, false)
        try store.setVaults([vault], connection: connection)
        XCTAssertEqual(try store.revision(connection), revision)
        vault.state = "unlocked"; try store.setVaults([vault], connection: connection)
        let restored = try store.reconcile(connection: connection, parent: "source", source: "Samba", files: [file], permissions: permissions)[0]
        XCTAssertEqual(restored.id, original.id)
    }
}
