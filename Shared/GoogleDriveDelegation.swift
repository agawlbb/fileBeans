import Foundation
import CryptoKit

struct SealedGoogleDriveGrant: Codable {
    let ephemeral: Data
    let sealed: Data
}

enum GoogleDriveDelegation {
    static func seal(account: Connection, server: CompanionCapabilities) async throws -> SealedGoogleDriveGrant {
        guard account.isGoogleDrive, let subject = account.googleAccountID,
              let encoded = server.driveTransferKey, let publicBytes = Data(base64Encoded: encoded),
              publicBytes.count == 32 else { throw APIError.incompatible }
        _ = try await GoogleDriveAuthorization.shared.accessToken(account)
        let grant = try GoogleDriveGrant.read(account.id)
        let scopes = grant.scope.split(separator: " ").map(String.init)
        guard scopes.contains("https://www.googleapis.com/auth/drive") else { throw APIError.forbidden }
        let ephemeral = Curve25519.KeyAgreement.PrivateKey()
        let remote = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: publicBytes)
        let shared = try ephemeral.sharedSecretFromKeyAgreement(with: remote)
        let info = Data(("fileBeans.native-drive.v1:" + (server.serverId ?? "")).utf8)
            + ephemeral.publicKey.rawRepresentation + publicBytes
        let key = shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: Data(), sharedInfo: info, outputByteCount: 32)
        let payload: [String: Any] = ["access_token": grant.accessToken, "refresh_token": grant.refreshToken,
            "expires": Int64(grant.expires.timeIntervalSince1970), "client_id": grant.clientID,
            "scopes": scopes, "account": subject]
        let data = try JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys])
        let box = try AES.GCM.seal(data, using: key, authenticating: info)
        guard let sealed = box.combined else { throw APIError.authentication }
        return SealedGoogleDriveGrant(ephemeral: ephemeral.publicKey.rawRepresentation, sealed: sealed)
    }
}
