import Foundation
import CryptoKit
import LocalAuthentication
import Security

enum VaultRecoveryCode {
    static func generate() throws -> String {
        var bytes = Data(count: 32)
        let result = bytes.withUnsafeMutableBytes { SecRandomCopyBytes(kSecRandomDefault, 32, $0.baseAddress!) }
        guard result == errSecSuccess else { throw failure("Could not generate an encryption key.") }
        return try encode(bytes)
    }
    static func encode(_ key: Data) throws -> String {
        guard key.count == 32 else { throw failure("Expected a 256-bit master key.") }
        let hex = key.map { String(format: "%02x", $0) }.joined()
        let prefix = "FBK1:" + hex
        let checksum = SHA256.hash(data: Data(prefix.utf8)).prefix(4).map { String(format: "%02x", $0) }.joined()
        return prefix + ":" + checksum
    }
    static func decode(_ code: String) throws -> Data {
        let parts = code.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 3, parts[0] == "FBK1", parts[1].count == 64, parts[2].count == 8 else {
            throw failure("Invalid recovery code. Paste the complete FBK1 code.")
        }
        let characters = Array(parts[1].utf8)
        guard characters.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }) else {
            throw failure("The key must contain lowercase hexadecimal characters.")
        }
        var data = Data()
        for offset in stride(from: 0, to: characters.count, by: 2) {
            guard let byte = UInt8(String(bytes: characters[offset..<offset+2], encoding: .utf8)!, radix: 16) else {
                throw failure("Invalid encryption key.")
            }
            data.append(byte)
        }
        guard try encode(data) == code.trimmingCharacters(in: .whitespacesAndNewlines) else {
            throw failure("Recovery code checksum does not match. Check your saved copy.")
        }
        return data
    }
    static func failure(_ text: String) -> NSError {
        NSError(domain: "fileBeans.VaultKey", code: 1, userInfo: [NSLocalizedDescriptionKey: text])
    }
}

/// App-only service: not shared with the Files extension, iCloud or device backups.
enum VaultKeyStore {
    private static var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: "fileBeans.VaultMasterKey.v1",
         kSecAttrAccount as String: "master",
         kSecAttrAccessGroup as String: Bundle.main.object(forInfoDictionaryKey: "VaultKeychainGroup") as! String,
         kSecAttrSynchronizable as String: false]
    }
    static func containsKey() -> Bool {
        var q = query
        q[kSecReturnAttributes as String] = true
        q[kSecUseAuthenticationUI as String] = kSecUseAuthenticationUIFail
        let status = SecItemCopyMatching(q as CFDictionary, nil)
        return status == errSecSuccess || status == errSecInteractionNotAllowed || status == errSecAuthFailed
    }
    static func save(_ code: String) async throws {
        let key = try VaultRecoveryCode.decode(code)
        let context = LAContext()
        try await context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics,
                                         localizedReason: "Protect your encryption key on this device")
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
                                                           .biometryCurrentSet, &error) else {
            throw error?.takeRetainedValue() as Error? ?? VaultRecoveryCode.failure("Biometric protection is unavailable.")
        }
        var q = query
        q[kSecAttrAccessControl as String] = access
        q[kSecValueData as String] = key
        // Never silently replace the only local copy of an existing master key.
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(status)) }
    }
    static func read() async throws -> String {
        let context = LAContext()
        context.localizedFallbackTitle = ""
        try await context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics,
                                         localizedReason: "Use your protected-folder encryption key")
        return try await Task.detached {
            var q = query
            q[kSecReturnData as String] = true
            q[kSecUseAuthenticationContext as String] = context
            var value: CFTypeRef?
            let status = SecItemCopyMatching(q as CFDictionary, &value)
            guard status == errSecSuccess, let key = value as? Data else {
                throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
            }
            return try VaultRecoveryCode.encode(key)
        }.value
    }
    static func remove() async throws {
        let context = LAContext()
        try await context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics,
                                         localizedReason: "Remove the encryption key from this device")
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}
