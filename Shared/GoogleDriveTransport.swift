import Foundation
import CryptoKit

/// Adapts the app's Drive operations to Google's API. This transport never uses
/// a FileBeans endpoint, server credential, LAN probe, or server-side OAuth grant.
final class GoogleDriveTransport {
    static let fields = "id,name,mimeType,size,version,modifiedTime,parents,driveId,webViewLink,hasThumbnail,thumbnailLink,trashed,capabilities"
    let connection: Connection
    let session: URLSession
    init(connection: Connection, configuration: URLSessionConfiguration = .ephemeral) {
        self.connection = connection
        let config = configuration.copy() as! URLSessionConfiguration
        config.httpCookieStorage = nil; config.urlCache = nil
        session = URLSession(configuration: config, delegate: GoogleDriveRedirects(), delegateQueue: nil)
    }
    func close() { session.invalidateAndCancel() }
    static func id(_ value: String) throws -> String {
        guard !value.isEmpty, value.utf8.count <= 256,
              value.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-") }) else { throw APIError.invalidURL }
        return value
    }
    static func googleURL(_ url: URL) -> Bool {
        guard url.scheme == "https", url.user == nil, url.password == nil,
              url.port == nil || url.port == 443, let host = url.host?.lowercased() else { return false }
        return host == "www.googleapis.com" || host == "content.googleapis.com" || host == "drive.usercontent.google.com" ||
            host.hasSuffix(".googleusercontent.com")
    }
    func url(_ path: String, query: [String: String] = [:], upload: Bool = false) throws -> URL {
        let base = upload ? "https://www.googleapis.com/upload/drive/v3/" : "https://www.googleapis.com/drive/v3/"
        var parts = URLComponents(string: base + path)!
        parts.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        guard let result = parts.url, Self.googleURL(result) else { throw APIError.invalidURL }
        return result
    }
    func authorized(_ route: String, method: String = "GET", headers: [String: String] = [:]) throws -> URLRequest {
        let parts = route.split(separator: "/").map(String.init)
        guard parts.count == 4, parts[0] == "drive", parts[1] == "files", parts[3] == "content" else { throw APIError.incompatible }
        let target = try url("files/" + Self.id(parts[2]), query: ["alt": "media", "supportsAllDrives": "true"])
        var request = URLRequest(url: target); request.httpMethod = method
        request.setValue("Bearer " + (try GoogleDriveGrant.read(connection.id)).accessToken, forHTTPHeaderField: "Authorization")
        for (key, value) in headers where key.lowercased() == "range" { request.setValue(value, forHTTPHeaderField: key) }
        return request
    }
    func refreshed(_ request: URLRequest) async throws -> URLRequest {
        guard let url = request.url, Self.googleURL(url) else { throw APIError.invalidURL }
        var result = request
        result.setValue("Bearer " + (try await GoogleDriveAuthorization.shared.accessToken(connection)), forHTTPHeaderField: "Authorization")
        return result
    }
    func send(_ target: URL, method: String = "GET", body: Data? = nil,
              headers: [String: String] = [:]) async throws -> (Data, HTTPURLResponse) {
        guard Self.googleURL(target) else { throw APIError.invalidURL }
        var request = URLRequest(url: target); request.httpMethod = method; request.httpBody = body
        for (key, value) in headers { request.setValue(value, forHTTPHeaderField: key) }
        request = try await refreshed(request)
        for _ in 0..<4 {
            let (data, response) = try await session.data(for: request)
            guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
            if [301,302,303,307,308].contains(http.statusCode), let location = http.value(forHTTPHeaderField: "Location"),
               let next = URL(string: location, relativeTo: request.url)?.absoluteURL {
                guard method == "GET", Self.googleURL(next) else { throw APIError.invalidURL }
                request.url = next
                continue
            }
            return (data, http)
        }
        throw URLError(.httpTooManyRedirects)
    }
    func checked(_ target: URL, method: String = "GET", body: Data? = nil,
                 headers: [String: String] = [:]) async throws -> Data {
        let (data, response) = try await send(target, method: method, body: body, headers: headers)
        guard (200..<300).contains(response.statusCode) else { throw Self.failure(data, response.statusCode) }
        return data
    }
    static func failure(_ data: Data, _ status: Int) -> Error {
        if status == 401 { return APIError.authentication }
        let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        let message = (object?["error"] as? [String: Any])?["message"] as? String
        return NSError(domain: "fileBeans.Google", code: status,
                       userInfo: [NSLocalizedDescriptionKey: message ?? "Google Drive request failed (\(status))."])
    }
    func object(_ path: String, query: [String: String] = [:]) async throws -> [String: Any] {
        guard let object = try JSONSerialization.jsonObject(with: await checked(url(path, query: query))) as? [String: Any] else { throw URLError(.badServerResponse) }
        return object
    }
    func metadata(_ id: String) async throws -> [String: Any] {
        try await object("files/" + Self.id(id), query: ["fields": Self.fields, "supportsAllDrives": "true"])
    }
    static func escaped(_ value: String) -> String {
        value.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "'", with: "\\'")
    }
    func listing(_ query: String, page: String = "") async throws -> Data {
        var values = ["q": query, "fields": "nextPageToken,files(\(Self.fields))", "pageSize": "100",
                      "supportsAllDrives": "true", "includeItemsFromAllDrives": "true"]
        if !page.isEmpty { values["pageToken"] = page }
        return try await checked(url("files", query: values))
    }
    func mutate(_ input: [String: Any], permanent: Bool, key: String?) async throws -> Data {
        guard let action = input["action"] as? String else { throw APIError.invalidURL }
        if action == "empty" {
            guard permanent else { throw APIError.forbidden }
            let scope = input["scope"] as? String ?? "root"
            let query = scope == "root" ? [:] : ["driveId": try Self.id(scope)]
            _ = try await checked(url("files/trash", query: query), method: "DELETE")
            return Data("{}".utf8)
        }
        if action == "mkdir" {
            guard let parent = input["parent"] as? String, let name = input["name"] as? String else { throw APIError.invalidURL }
            _ = try await createFolder(name: name, parent: parent, operation: key ?? UUID().uuidString)
            return Data("{}".utf8)
        }
        guard let file = input["fileId"] as? String else { throw APIError.invalidURL }
        let id = try Self.id(file)
        let current: [String: Any]
        do { current = try await metadata(id) }
        catch { if action == "purge", permanent, (error as NSError).code == 404 { return Data("{}".utf8) }; throw error }
        if action == "rename", let name = input["name"] as? String, current["name"] as? String == name { return Data("{}".utf8) }
        if action == "trash", current["trashed"] as? Bool == true { return Data("{}".utf8) }
        if action == "restore", current["trashed"] as? Bool == false { return Data("{}".utf8) }
        if let version = input["version"] as? String, !version.isEmpty, version != current["version"] as? String {
            throw NSError(domain: "fileBeans.Google", code: 409, userInfo: [NSLocalizedDescriptionKey: "The file changed. Refresh and try again."])
        }
        let target = try url("files/" + id, query: ["supportsAllDrives": "true"])
        if action == "purge" {
            guard permanent else { throw APIError.forbidden }
            _ = try await checked(target, method: "DELETE")
        } else {
            let change: [String: Any]
            switch action {
            case "rename": guard let name = input["name"] as? String, !name.isEmpty else { throw APIError.invalidURL }; change = ["name": name]
            case "trash": change = ["trashed": true]
            case "restore": change = ["trashed": false]
            default: throw APIError.incompatible
            }
            _ = try await checked(target, method: "PATCH", body: JSONSerialization.data(withJSONObject: change), headers: ["Content-Type": "application/json"])
        }
        return Data("{}".utf8)
    }
    func createFolder(name: String, parent: String, operation: String) async throws -> String {
        try await publishCopy(source: nil, name: name, parent: parent, operation: operation)
    }
    /// Reconcile a lost publication response by an account-local operation marker
    /// before replaying. Only Google performs the copy; no payload traverses iOS.
    func publishCopy(source: String?, name: String, parent: String, operation: String) async throws -> String {
        let parent = try Self.id(parent)
        let marker = SHA256.hash(data: Data(operation.utf8)).map { String(format: "%02x", $0) }.joined()
        let q = "'\(Self.escaped(parent))' in parents and trashed=false and appProperties has { key='fileBeansOperation' and value='\(marker)' }"
        let found = try JSONSerialization.jsonObject(with: await listing(q)) as? [String: Any]
        if let files = found?["files"] as? [[String: Any]], let id = files.first?["id"] as? String {
            guard files.count == 1 else { throw APIError.forbidden }; return id
        }
        var body: [String: Any] = ["name": name, "parents": [parent], "appProperties": ["fileBeansOperation": marker]]
        let path: String
        if let source { path = "files/" + (try Self.id(source)) + "/copy" }
        else { path = "files"; body["mimeType"] = "application/vnd.google-apps.folder" }
        let data = try await checked(url(path, query: ["supportsAllDrives": "true", "fields": "id"]), method: "POST",
                                     body: JSONSerialization.data(withJSONObject: body), headers: ["Content-Type": "application/json"])
        guard let value = try JSONSerialization.jsonObject(with: data) as? [String: Any], let id = value["id"] as? String else { throw URLError(.badServerResponse) }
        return id
    }
    func request(_ route: String, method: String, body: Data?, key: String?, permanent: Bool,
                 query: [String: String], headers: [String: String]) async throws -> Data {
        guard connection.isGoogleDrive else { throw APIError.incompatible }
        if route == "drive/disconnect", method == "POST" { try GoogleTokenStore.remove(connection.id); return Data("{}".utf8) }
        _ = try await GoogleDriveAuthorization.shared.accessToken(connection)
        if route == "capabilities" {
            return try JSONSerialization.data(withJSONObject: ["version": 1, "roots": [], "driveConnected": true,
                "driveWriteAccess": true, "normalizationEnabled": false,
                "features": ["driveWrite", "driveUploads", "driveThumbnails", "driveMedia", "driveDestinations"]])
        }
        if route.hasPrefix("jobs") {
            return try await GoogleDriveJobs.shared.request(transport: self, route: route, method: method, body: body, key: key)
        }
        if route.hasPrefix("drive/uploads") {
            return try await GoogleDriveUploads.shared.request(transport: self, route: route, method: method, body: body, key: key)
        }
        if route == "drive/disconnect", method == "POST" {
            try GoogleTokenStore.remove(connection.id); return Data("{}".utf8)
        }
        if route == "drive/files" {
            let folder = query["folder"] ?? "root"
            let filter = folder == "shared" ? "sharedWithMe=true and trashed=false" : "'\(Self.escaped(try Self.id(folder)))' in parents and trashed=false"
            return try await listing(filter, page: query["page"] ?? "")
        }
        if route == "drive/drives" {
            var q = ["pageSize": "100", "fields": "nextPageToken,drives(id,name)"]
            if let page = query["page"], !page.isEmpty { q["pageToken"] = page }
            return try await checked(url("drives", query: q))
        }
        if route == "drive/trash" {
            var filter = "trashed=true"
            let scope = query["scope"] ?? "root"
            if scope == "root" { filter += " and 'me' in owners" }
            else { filter += " and driveId='\(Self.escaped(try Self.id(scope)))'" }
            return try await listing(filter, page: query["page"] ?? "")
        }
        if route == "drive/mutations", method == "POST", let body,
           let input = try JSONSerialization.jsonObject(with: body) as? [String: Any] {
            return try await mutate(input, permanent: permanent, key: key)
        }
        let parts = route.split(separator: "/").map(String.init)
        if parts.count >= 3, parts[0] == "drive", parts[1] == "files" {
            let id = try Self.id(parts[2])
            if parts.count == 3 { return try JSONSerialization.data(withJSONObject: await metadata(id)) }
            if parts.count == 4, parts[3] == "content" {
                return try await checked(url("files/" + id, query: ["alt": "media", "supportsAllDrives": "true"]), headers: headers.filter { $0.key.lowercased() == "range" })
            }
            if parts.count == 4, parts[3] == "thumbnail" {
                let file = try await metadata(id)
                guard let link = file["thumbnailLink"] as? String, let url = URL(string: link), Self.googleURL(url) else { throw APIError.missing }
                return try await checked(url)
            }
        }
        throw APIError.incompatible
    }
    func download(_ route: String) async throws -> URL {
        var request = try await refreshed(authorized(route))
        for _ in 0..<4 {
            let (file, response) = try await session.download(for: request)
            guard let http = response as? HTTPURLResponse else { try? FileManager.default.removeItem(at: file); throw URLError(.badServerResponse) }
            if [301,302,303,307,308].contains(http.statusCode), let location = http.value(forHTTPHeaderField: "Location"),
               let url = URL(string: location, relativeTo: request.url)?.absoluteURL, Self.googleURL(url) {
                try? FileManager.default.removeItem(at: file); request.url = url; continue
            }
            guard (200..<300).contains(http.statusCode) else {
                let data = (try? Data(contentsOf: file)) ?? Data(); try? FileManager.default.removeItem(at: file)
                throw Self.failure(data, http.statusCode)
            }
            let target = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try FileManager.default.moveItem(at: file, to: target); return target
        }
        throw URLError(.httpTooManyRedirects)
    }
}
