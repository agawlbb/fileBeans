import Foundation

enum GoogleTokenStore {
    private static var group: String {
        get throws {
            guard let value = Bundle.main.object(forInfoDictionaryKey: "GoogleKeychainGroup") as? String, !value.isEmpty else { throw APIError.authentication }
            return value
        }
    }
    static func read(_ id: UUID) throws -> String { try TokenStore.read(id, group: group) }
    static func save(_ value: String, for id: UUID) throws { try TokenStore.save(value, for: id, group: group) }
    static func remove(_ id: UUID) throws { try TokenStore.remove(id, group: group) }
}


struct GoogleDriveGrant: Codable {
    var accessToken: String
    var refreshToken: String
    var expires: Date
    let clientID: String
    var scope: String

    static func read(_ id: UUID) throws -> GoogleDriveGrant {
        guard let data = try GoogleTokenStore.read(id).data(using: .utf8) else { throw APIError.authentication }
        return try JSONDecoder().decode(Self.self, from: data)
    }
    func save(_ id: UUID) throws {
        guard !refreshToken.isEmpty,
              let text = String(data: try JSONEncoder().encode(self), encoding: .utf8) else { throw APIError.authentication }
        try GoogleTokenStore.save(text, for: id)
    }
}

/// OAuth credentials belong to an explicitly added Google account, never a
/// FileBeans server connection. Refreshes are shared by app/Share consumers via
/// Keychain; an in-process task coalesces simultaneous requests for one account.
actor GoogleDriveAuthorization {
    static let shared = GoogleDriveAuthorization()
    private var refreshing: [UUID: Task<String, Error>] = [:]

    func accessToken(_ connection: Connection) async throws -> String {
        guard connection.isGoogleDrive else { throw APIError.incompatible }
        let grant = try GoogleDriveGrant.read(connection.id)
        if grant.expires.timeIntervalSinceNow > 90 { return grant.accessToken }
        if let task = refreshing[connection.id] { return try await task.value }
        let id = connection.id
        let task = Task<String, Error> {
            let response = try await Self.exchange([
                "client_id": grant.clientID, "refresh_token": grant.refreshToken,
                "grant_type": "refresh_token"
            ])
            guard let token = response["access_token"] as? String,
                  let seconds = response["expires_in"] as? Double else { throw APIError.authentication }
            var next = grant
            next.accessToken = token; next.expires = Date().addingTimeInterval(seconds)
            if let refresh = response["refresh_token"] as? String { next.refreshToken = refresh }
            if let scope = response["scope"] as? String { next.scope = scope }
            try next.save(id)
            return token
        }
        refreshing[id] = task
        defer { refreshing.removeValue(forKey: id) }
        return try await task.value
    }

    static func exchange(_ values: [String: String]) async throws -> [String: Any] {
        var parts = URLComponents()
        parts.queryItems = values.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        var request = URLRequest(url: URL(string: "https://oauth2.googleapis.com/token")!)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.httpBody = parts.percentEncodedQuery?.data(using: .utf8)
        let session = URLSession(configuration: .ephemeral, delegate: GoogleDriveRedirects(), delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200,
              let result = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw NSError(domain: "fileBeans.Google", code: 401,
                          userInfo: [NSLocalizedDescriptionKey: "Google sign-in expired. Reconnect this Google Drive source."])
        }
        return result
    }
}

/// Never forward an account token to a redirect target. API media and upload
/// requests use validated Google URLs and can obtain a new authenticated request.
final class GoogleDriveRedirects: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
