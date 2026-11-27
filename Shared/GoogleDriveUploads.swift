import Foundation
import CryptoKit

private struct GoogleUploadSession: Codable {
    let id: UUID
    let account: UUID
    let fileID: String
    let name: String
    let parent: String
    let size: Int64
    var location: URL?
    var completed = false
    func save() throws {
        let data = try JSONEncoder().encode(self)
        guard let text = String(data: data, encoding: .utf8) else { throw APIError.incompatible }
        try GoogleTokenStore.save(text, for: id)
    }
    static func read(_ id: String, connection: Connection) throws -> GoogleUploadSession {
        guard let uuid = UUID(uuidString: id), let data = try GoogleTokenStore.read(uuid).data(using: .utf8) else { throw APIError.authentication }
        let value = try JSONDecoder().decode(Self.self, from: data)
        guard value.account == connection.id else { throw APIError.authentication }
        return value
    }
    func reply(received: Int64) throws -> Data {
        try JSONSerialization.data(withJSONObject: ["id": id.uuidString, "name": name, "parent": parent,
            "size": size, "received": received, "state": completed ? "completed" : "uploading"])
    }
}

actor GoogleDriveUploads {
    static let shared = GoogleDriveUploads()
    static func authorized(connection: Connection, id: String, headers: [String: String]) throws -> URLRequest {
        let state = try GoogleUploadSession.read(id, connection: connection)
        guard !state.completed, let url = state.location, GoogleDriveTransport.googleURL(url) else { throw APIError.invalidURL }
        var request = URLRequest(url: url); request.httpMethod = "PUT"
        request.setValue("Bearer " + (try GoogleDriveGrant.read(connection.id)).accessToken, forHTTPHeaderField: "Authorization")
        for (name, value) in headers where ["content-type", "content-range"].contains(name.lowercased()) {
            request.setValue(value, forHTTPHeaderField: name)
        }
        return request
    }
    private func identity(_ connection: Connection, _ key: String) -> UUID {
        let digest = SHA256.hash(data: Data(("google-upload:" + connection.id.uuidString + ":" + key).utf8))
        let hex = digest.prefix(16).map { String(format: "%02x", $0) }.joined()
        let parts = [8,4,4,4,12]; var offset = hex.startIndex
        let text = parts.map { count -> String in let end = hex.index(offset, offsetBy: count); defer { offset = end }; return String(hex[offset..<end]) }.joined(separator: "-")
        return UUID(uuidString: text)!
    }
    func request(transport: GoogleDriveTransport, route: String, method: String, body: Data?, key: String?) async throws -> Data {
        let connection = transport.connection
        if route == "drive/uploads", method == "POST" {
            guard let key, let body, let input = try JSONSerialization.jsonObject(with: body) as? [String: Any],
                  let name = input["name"] as? String, !name.isEmpty,
                  let parent = input["parent"] as? String, let size = input["size"] as? Int64, size >= 0 else { throw APIError.invalidURL }
            let id = identity(connection, key)
            if let existing = try? GoogleUploadSession.read(id.uuidString, connection: connection) {
                guard existing.name == name, existing.parent == parent, existing.size == size else { throw APIError.forbidden }
                return try await status(existing, transport)
            }
            _ = try GoogleDriveTransport.id(parent)
            let generated = try await transport.object("files/generateIds", query: ["count": "1", "space": "drive", "type": "files"])
            guard let fileID = (generated["ids"] as? [String])?.first else { throw APIError.incompatible }
            let metadata: [String: Any] = ["id": fileID, "name": name, "parents": [parent]]
            let (_, response) = try await transport.send(transport.url("files", query: ["uploadType": "resumable", "supportsAllDrives": "true"], upload: true),
                method: "POST", body: JSONSerialization.data(withJSONObject: metadata),
                headers: ["Content-Type": "application/json", "X-Upload-Content-Length": String(size), "X-Upload-Content-Type": "application/octet-stream"])
            guard (200..<300).contains(response.statusCode), let value = response.value(forHTTPHeaderField: "Location"),
                  let location = URL(string: value), GoogleDriveTransport.googleURL(location) else { throw APIError.status(response.statusCode) }
            let state = GoogleUploadSession(id: id, account: connection.id, fileID: fileID,
                name: name, parent: parent, size: size, location: location)
            try state.save()
            return try await status(state, transport)
        }
        let parts = route.split(separator: "/").map(String.init)
        guard parts.count >= 3, parts[0] == "drive", parts[1] == "uploads" else { throw APIError.incompatible }
        let state = try GoogleUploadSession.read(parts[2], connection: connection)
        if parts.count == 4, parts[3] == "cancel", method == "POST" {
            // A published user file is never deleted by an upload cancellation.
            // Unfinished Google resumable sessions expire without publication.
            try GoogleTokenStore.remove(state.id)
            return Data("{}".utf8)
        }
        if parts.count == 4, parts[3] == "acknowledge", method == "POST", state.completed {
            try GoogleTokenStore.remove(state.id); return Data("{}".utf8)
        }
        guard parts.count == 3, method == "GET" else { throw APIError.incompatible }
        return try await status(state, transport)
    }
    private func status(_ value: GoogleUploadSession, _ transport: GoogleDriveTransport) async throws -> Data {
        if value.completed { return try value.reply(received: value.size) }
        guard let location = value.location else { throw APIError.incompatible }
        let (data, response) = try await transport.send(location, method: "PUT", body: Data(),
            headers: ["Content-Range": "bytes */\(value.size)", "Content-Length": "0"])
        if response.statusCode == 200 || response.statusCode == 201 {
            var complete = value; complete.completed = true; complete.location = nil
            try complete.save()
            return try complete.reply(received: complete.size)
        }
        guard response.statusCode == 308 else { throw GoogleDriveTransport.failure(data, response.statusCode) }
        let range = response.value(forHTTPHeaderField: "Range") ?? ""
        let received = range.split(separator: "-").last.flatMap { Int64($0) }.map { $0 + 1 } ?? 0
        guard received >= 0, received <= value.size else { throw APIError.incompatible }
        return try value.reply(received: received)
    }
}
