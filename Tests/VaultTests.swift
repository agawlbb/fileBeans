import XCTest
@testable import myFiles

final class VaultTests: XCTestCase {
    func testRecoveryCodeContainsTheActualMasterKey() throws {
        let key = Data(repeating: 0x42, count: 32)
        let code = try VaultRecoveryCode.encode(key)
        XCTAssertEqual(String(code.split(separator: ":")[1]), String(repeating: "42", count: 32))
        XCTAssertEqual(try VaultRecoveryCode.decode("\n" + code + "\n"), key)
    }
    func testRecoveryCodeRejectsCorruptionAndUnsupportedVersions() throws {
        let code = try VaultRecoveryCode.encode(Data(repeating: 0x42, count: 32))
        for invalid in ["", code + "x", code.replacingOccurrences(of: "FBK1", with: "FBK2"),
                        code.replacingOccurrences(of: "4242", with: "42ff"), String(code.dropLast()) + "z"] {
            XCTAssertThrowsError(try VaultRecoveryCode.decode(invalid))
        }
        XCTAssertThrowsError(try VaultRecoveryCode.encode(Data(count: 31)))
    }
    func testVaultStatusDoesNotRequireKeyMaterial() throws {
        let json = #"[{"id":"archive","source":"Protected","state":"locked","initialized":true}]"#
        let vault = try JSONDecoder().decode([ProtectedVault].self, from: Data(json.utf8))[0]
        XCTAssertEqual(vault.state, "locked")
        XCTAssertTrue(vault.initialized)
    }
    func testProtectionUsesSourceAndPathBoundaries() throws {
        let vault = ProtectedVault(id: "private", source: "disk", state: "locked", initialized: true,
                                   path: "/Photos/Private", name: "Private")
        XCTAssertTrue(vault.contains(source: "disk", path: "/Photos/Private"))
        XCTAssertTrue(vault.contains(source: "disk", path: "/Photos/Private/file.jpg"))
        XCTAssertFalse(vault.contains(source: "other", path: "/Photos/Private"))
        XCTAssertFalse(vault.contains(source: "disk", path: "/Photos/PrivateCopy/file.jpg"))
        XCTAssertFalse(vault.contains(source: "disk", path: "/Photos"))
        XCTAssertEqual(vault.displayName, "Private")
    }
    func testOldCatalogDecodesWithoutProtectionFields() throws {
        let json = #"{"connections":[],"items":{},"enumerated":{},"revisions":{},"deleted":{},"boundaries":{}}"#
        let catalog = try JSONDecoder().decode(Catalog.self, from: Data(json.utf8))
        XCTAssertNil(catalog.protectedFolders)
    }
}
