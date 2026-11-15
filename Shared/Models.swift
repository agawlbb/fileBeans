import Foundation

struct Connection: Codable, Identifiable, Hashable {
    var id: UUID = UUID()
    var name: String
    var url: URL
}

struct Permissions: Codable, Hashable {
    var view: Bool = false
    var download: Bool = false
    var modify: Bool = false
    var create: Bool = false
    var delete: Bool = false

    init(view: Bool = false, download: Bool = false, modify: Bool = false,
         create: Bool = false, delete: Bool = false) {
        self.view = view; self.download = download; self.modify = modify
        self.create = create; self.delete = delete
    }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        view = try c.decodeIfPresent(Bool.self, forKey: .view) ?? false
        download = try c.decodeIfPresent(Bool.self, forKey: .download) ?? false
        modify = try c.decodeIfPresent(Bool.self, forKey: .modify) ?? false
        create = try c.decodeIfPresent(Bool.self, forKey: .create) ?? false
        delete = try c.decodeIfPresent(Bool.self, forKey: .delete) ?? false
    }
}

struct Scope: Codable, Hashable, Identifiable {
    var name: String
    var scope: String
    var permissions: Permissions
    var id: String { name + ":" + scope }
}

struct User: Decodable {
    var scopes: [Scope]
}

struct RemoteFile: Codable, Hashable {
    var name: String
    var path: String
    var type: String
    var size: Int64?
    var modified: String?
    var hasPreview: Bool?
    var viewToken: String?
    var isDirectory: Bool { type == "directory" }
    var isMedia: Bool { type.hasPrefix("video/") || type.hasPrefix("audio/") }
    var date: Date? {
        guard let modified else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: modified) ?? ISO8601DateFormatter().date(from: modified)
    }
    var version: Data { Data("\(modified ?? ""):\(size ?? 0)".utf8) }
}

struct Resource: Decodable {
    var name: String
    var path: String
    var type: String
    var size: Int64?
    var modified: String?
    var hasPreview: Bool?
    var viewToken: String?
    var folders: [RemoteFile]?
    var files: [RemoteFile]?
    var children: [RemoteFile] { (folders ?? []) + (files ?? []) }
    var file: RemoteFile {
        RemoteFile(name: name, path: path, type: type, size: size, modified: modified,
                   hasPreview: hasPreview, viewToken: viewToken)
    }
}

enum FilePath {
    static func normalized(_ path: String) -> String {
        "/" + path.split(separator: "/").map(String.init).joined(separator: "/")
    }
    static func child(_ name: String, in parent: String) throws -> String {
        guard !name.isEmpty, name != ".", name != "..", !name.contains("/"),
              !name.contains("\0") else { throw APIError.invalidName }
        return normalized(parent + "/" + name)
    }
    static func parent(_ path: String) -> String {
        normalized((path as NSString).deletingLastPathComponent)
    }
    static func contains(_ path: String, in root: String) -> Bool {
        let p = normalized(path), r = normalized(root)
        return r == "/" || p == r || p.hasPrefix(r + "/")
    }
}

enum APIError: LocalizedError {
    case invalidURL, invalidName, authentication, forbidden, missing, conflict, incompatible, status(Int)
    var errorDescription: String? {
        switch self {
        case .invalidURL: return "Enter an HTTPS server URL without credentials, query parameters, or a fragment."
        case .invalidName: return "Choose a name without slashes."
        case .authentication: return "Your API token expired or was rejected. Update this connection."
        case .forbidden: return "You don’t have permission for this action."
        case .missing: return "This item is no longer available."
        case .conflict: return "An item already exists or changed on the server. Refresh before trying again."
        case .incompatible: return "This connection requires File Browser Quantum v2 with API access enabled."
        case .status(let code): return "The server could not complete the request (\(code))."
        }
    }
}
