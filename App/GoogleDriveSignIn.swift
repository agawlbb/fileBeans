import AuthenticationServices
import CryptoKit
import Security
import UIKit

@MainActor
final class GoogleDriveSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    static let shared = GoogleDriveSignIn()
    private var session: ASWebAuthenticationSession?
    private var anchor: ASPresentationAnchor?

    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        anchor!
    }
    private func random() throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else { throw APIError.authentication }
        return Self.base64URL(Data(bytes))
    }
    private static func base64URL(_ data: Data) -> String {
        data.base64EncodedString().replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
    }
    func connect(existing: Connection? = nil) async throws -> Connection {
        guard session == nil else { throw APIError.forbidden }
        guard let clientID = Bundle.main.object(forInfoDictionaryKey: "GoogleIOSClientID") as? String,
              clientID.hasSuffix(".apps.googleusercontent.com"), !clientID.contains("$(") else {
            throw NSError(domain: "fileBeans.Google", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: "Google sign-in is not configured for this build."])
        }
        guard let window = UIApplication.shared.connectedScenes.compactMap({ $0 as? UIWindowScene })
            .filter({ $0.activationState == .foregroundActive }).flatMap(\.windows).first(where: \.isKeyWindow) else {
            throw APIError.forbidden
        }
        anchor = window
        defer { session = nil; anchor = nil }
        let scheme = clientID.split(separator: ".").reversed().joined(separator: ".")
        let redirect = scheme + ":/oauth2redirect"
        let verifier = try random(), state = try random()
        let challenge = Self.base64URL(Data(SHA256.hash(data: Data(verifier.utf8))))
        var url = URLComponents(string: "https://accounts.google.com/o/oauth2/v2/auth")!
        let parameters = ["client_id": clientID, "redirect_uri": redirect, "response_type": "code",
                          "scope": "openid email https://www.googleapis.com/auth/drive",
                          "state": state, "code_challenge": challenge, "code_challenge_method": "S256",
                          "access_type": "offline", "prompt": "consent select_account"]
        url.queryItems = parameters.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        let callback: URL = try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                let auth = ASWebAuthenticationSession(url: url.url!, callbackURLScheme: scheme) { callback, error in
                    if let callback { continuation.resume(returning: callback) }
                    else { continuation.resume(throwing: error ?? APIError.authentication) }
                }
                auth.presentationContextProvider = self
                auth.prefersEphemeralWebBrowserSession = false
                session = auth
                if !auth.start() { continuation.resume(throwing: APIError.authentication) }
            }
        } onCancel: { Task { @MainActor in self.session?.cancel() } }
        guard callback.scheme == scheme, callback.path == "/oauth2redirect",
              let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems else { throw APIError.authentication }
        let grouped = Dictionary(grouping: items, by: \.name)
        guard grouped["state"]?.count == 1, grouped["state"]?.first?.value == state,
              grouped["code"]?.count == 1, let code = grouped["code"]?.first?.value else { throw APIError.authentication }
        let tokens = try await GoogleDriveAuthorization.exchange([
            "client_id": clientID, "redirect_uri": redirect, "code": code,
            "code_verifier": verifier, "grant_type": "authorization_code"
        ])
        guard let access = tokens["access_token"] as? String,
              let seconds = tokens["expires_in"] as? Double else { throw APIError.authentication }
        var request = URLRequest(url: URL(string: "https://openidconnect.googleapis.com/v1/userinfo")!)
        request.setValue("Bearer " + access, forHTTPHeaderField: "Authorization")
        let client = URLSession(configuration: .ephemeral, delegate: GoogleDriveRedirects(), delegateQueue: nil)
        defer { client.invalidateAndCancel() }
        let (data, response) = try await client.data(for: request)
        guard (response as? HTTPURLResponse)?.statusCode == 200,
              let profile = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let account = profile["sub"] as? String, let email = profile["email"] as? String else { throw APIError.authentication }
        if let existing, existing.googleAccountID != account {
            throw NSError(domain: "fileBeans.Google", code: 409,
                          userInfo: [NSLocalizedDescriptionKey: "Choose the account belonging to this source, or add a new Google Drive source."])
        }
        let store = try SharedStore()
        var connection = existing ?? (try store.connections().first { $0.isGoogleDrive && $0.googleAccountID == account })
            ?? Connection(name: email, url: URL(string: "https://www.googleapis.com/drive/v3")!)
        connection.kind = .googleDrive; connection.companionURL = nil; connection.companionTokenID = nil
        connection.googleAccountID = account
        let old = try? GoogleDriveGrant.read(connection.id)
        let grant = GoogleDriveGrant(accessToken: access,
            refreshToken: tokens["refresh_token"] as? String ?? old?.refreshToken ?? "",
            expires: Date().addingTimeInterval(seconds), clientID: clientID,
            scope: tokens["scope"] as? String ?? "https://www.googleapis.com/auth/drive")
        guard Set(grant.scope.split(separator: " ").map(String.init)).contains("https://www.googleapis.com/auth/drive") else {
            throw NSError(domain: "fileBeans.Google", code: 403, userInfo: [NSLocalizedDescriptionKey: "Drive access was not granted. Sign in again and allow Google Drive access."])
        }
        try grant.save(connection.id)
        do { try store.save(connection) }
        catch {
            if let old { try? old.save(connection.id) } else { try? GoogleTokenStore.remove(connection.id) }
            throw error
        }
        return connection
    }
}
