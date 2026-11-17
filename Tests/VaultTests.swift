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
}
