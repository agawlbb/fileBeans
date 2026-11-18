import XCTest
@testable import myFiles

@MainActor
final class VaultSessionTests: XCTestCase {
    private var defaults: UserDefaults!
    private var suite: String!
    override func setUp() async throws {
        suite = "fileBeans.VaultTests." + UUID().uuidString
        defaults = UserDefaults(suiteName: suite)!
    }
    override func tearDown() async throws { defaults.removePersistentDomain(forName: suite) }

    func testNewKeyIsSecuredBeforeDisplayAndCanResumeAfterTermination() async throws {
        var stored: String?
        let session = VaultKeySession(defaults: defaults, containsKey: { false }, saveKey: { value in
            stored = value
        }, readKey: { stored! }, removeKey: {})
        await session.generate()
        XCTAssertEqual(session.code, stored)
        XCTAssertTrue(session.hasKey)
        XCTAssertFalse(session.backupConfirmed)
        // View recreation and app switching keep the same session and key.
        let original = session.code
        XCTAssertFalse(original.isEmpty)
        // A fresh process has no visible code and must read the secure copy.
        var reads = 0
        let resumed = VaultKeySession(defaults: defaults, containsKey: { stored != nil }, saveKey: { _ in
            XCTFail("Must not replace a pending key")
        }, readKey: { reads += 1; return stored! }, removeKey: {})
        XCTAssertTrue(resumed.hasKey)
        XCTAssertFalse(resumed.backupConfirmed)
        XCTAssertTrue(resumed.code.isEmpty)
        await resumed.reveal()
        XCTAssertEqual(resumed.code, original)
        XCTAssertEqual(reads, 1)
        resumed.confirmBackup()
        let relaunched = VaultKeySession(defaults: defaults, containsKey: { true }, saveKey: { _ in },
                                         readKey: { stored! }, removeKey: {})
        XCTAssertTrue(relaunched.backupConfirmed)
        XCTAssertFalse(defaults.dictionaryRepresentation().values.contains { ($0 as? String) == original })
    }


    func testImportStoresTheSameCanonicalKeyAfterCrossDevicePaste() async throws {
        let canonical = try VaultRecoveryCode.encode(Data(repeating: 0xAB, count: 32))
        var saved: String?
        let session = VaultKeySession(defaults: defaults, containsKey: { false },
            saveKey: { saved = $0 }, readKey: { canonical }, removeKey: {})
        session.importCode = canonical.uppercased().replacingOccurrences(of: ":", with: ":\n")
        await session.importKey()
        XCTAssertNil(session.error)
        XCTAssertTrue(session.hasKey)
        XCTAssertEqual(saved, canonical)
        XCTAssertEqual(session.code, canonical)
        XCTAssertTrue(session.importCode.isEmpty)
    }

    func testFailedSecureStorageNeverDisplaysGeneratedKey() async {
        let session = VaultKeySession(defaults: defaults, containsKey: { false }, saveKey: { _ in
            throw CancellationError()
        }, readKey: { "" }, removeKey: {})
        await session.generate()
        XCTAssertFalse(session.hasKey)
        XCTAssertTrue(session.code.isEmpty)
        XCTAssertFalse(session.backupConfirmed)
        XCTAssertNotNil(session.error)
    }

    func testUnlockRequiresBiometricReadEvenWhenCodeIsVisible() async throws {
        let code = try VaultRecoveryCode.encode(Data(repeating: 0x42, count: 32))
        var reads = 0
        let session = VaultKeySession(defaults: defaults, containsKey: { true }, saveKey: { _ in },
                                     readKey: { reads += 1; throw CancellationError() }, removeKey: {})
        session.code = code
        do { _ = try await session.authorizedCode(); XCTFail("Expected authentication cancellation") }
        catch is CancellationError {} catch { XCTFail("Unexpected error: \(error)") }
        XCTAssertEqual(reads, 1)
        XCTAssertEqual(session.code, code)
    }

    func testBackupAcknowledgmentGatesUseOfPendingKey() async {
        var reads = 0
        let session = VaultKeySession(defaults: defaults, containsKey: { false }, saveKey: { _ in },
                                     readKey: { reads += 1; return "" }, removeKey: {})
        await session.generate()
        do { _ = try await session.authorizedCode(); XCTFail("Expected backup requirement") } catch {}
        XCTAssertEqual(reads, 0)
    }

    func testCreationDraftResumesSameRequestAndChangesIDWhenEdited() {
        let connection = UUID()
        let parent = StoredItem(id: "parent", parent: "root", source: "disk",
                                file: RemoteFile(name: "Photos", path: "/Photos", type: "directory"),
                                permissions: Permissions())
        let draft = ProtectedFolderDraft(defaults: defaults)
        draft.begin(connection: connection, parent: parent)
        draft.name = "Private"
        let request = draft.requestID
        let restored = ProtectedFolderDraft(defaults: defaults)
        restored.begin(connection: connection, parent: parent)
        XCTAssertEqual(restored.name, "Private")
        XCTAssertEqual(restored.requestID, request)
        restored.name = "Personal"
        XCTAssertNotEqual(restored.requestID, request)
        restored.finish()
        let cleared = ProtectedFolderDraft(defaults: defaults)
        cleared.begin(connection: connection, parent: parent)
        XCTAssertEqual(cleared.name, "")
    }
}
