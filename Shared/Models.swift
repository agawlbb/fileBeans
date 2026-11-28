import Foundation

enum SourceKind: String, Codable, CaseIterable { case server, googleDrive }

struct Connection: Codable, Identifiable, Hashable {
    var id: UUID = UUID()
    var name: String
    var url: URL
    var companionURL: URL?
    var companionTokenID: UUID?
    var localURL: URL? = nil
    var localFingerprint: String? = nil
    var serverID: String? = nil
    var kind: SourceKind? = nil
    var googleAccountID: String? = nil
    var isGoogleDrive: Bool { kind == .googleDrive }
    // Existing connections already identify the companion, which now hosts
    // file access too. Keep connection/domain IDs and stored credentials.
    var filesystemURL: URL { companionURL ?? url }
    var filesystemTokenID: UUID { companionTokenID ?? id }
    var unified: Connection {
        guard !isGoogleDrive else { return self }
        var value = self
        if name.trimmingCharacters(in: .whitespacesAndNewlines).caseInsensitiveCompare("File Browser Quantum") == .orderedSame {
            let endpoint = filesystemURL
            if serverID == "filebeans-vps" || endpoint.host?.lowercased() == "files.coffeybean.com" && endpoint.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")) == "companion" {
                value.name = "VPS"
            } else if serverID == "filebeans-pi" {
                value.name = "Pi"
            } else {
                value.name = "FileBeans"
            }
        }
        value.companionURL = filesystemURL
        value.companionTokenID = filesystemTokenID
        return value
    }
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
    var version: Int
    var scopes: [Scope]
}

struct RemoteFile: Codable, Hashable {
    var name: String
    var path: String
    var type: String
    var size: Int64?
    var modified: String?
    var hasPreview: Bool?
    var revision: String? = nil
    var archiveLink: String? = nil
    var archiveRole: String? = nil
    var archiveRoot: Bool? = nil
    var archiveLibrary: ArchiveEndpoint? = nil
    var isDirectory: Bool { type == "directory" }
    var isMedia: Bool { type.hasPrefix("video/") || type.hasPrefix("audio/") }
    var date: Date? {
        guard let modified else { return nil }
        return FileDates.parse(modified)
    }
    var version: Data { Data((revision ?? "\(modified ?? ""):\(size ?? 0)").utf8) }
}

struct Resource: Decodable {
    var name: String
    var path: String
    var type: String
    var size: Int64?
    var modified: String?
    var hasPreview: Bool?
    var revision: String?
    var children: [RemoteFile]
    var archiveLink: String?
    var archiveRole: String?
    var archiveRoot: Bool?
    var archiveLibrary: ArchiveEndpoint?
    private enum CodingKeys: String, CodingKey {
        case name, path, type, size, modified, hasPreview, revision, children, archiveLink, archiveRole, archiveRoot, archiveLibrary
    }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        path = FilePath.normalized(try c.decode(String.self, forKey: .path))
        type = try c.decode(String.self, forKey: .type)
        size = try c.decodeIfPresent(Int64.self, forKey: .size)
        modified = try c.decodeIfPresent(String.self, forKey: .modified)
        hasPreview = try c.decodeIfPresent(Bool.self, forKey: .hasPreview)
        revision = try c.decodeIfPresent(String.self, forKey: .revision)
        archiveLink = try c.decodeIfPresent(String.self, forKey: .archiveLink)
        archiveRole = try c.decodeIfPresent(String.self, forKey: .archiveRole)
        archiveRoot = try c.decodeIfPresent(Bool.self, forKey: .archiveRoot)
        archiveLibrary = try c.decodeIfPresent(ArchiveEndpoint.self, forKey: .archiveLibrary)
        children = try c.decodeIfPresent([RemoteFile].self, forKey: .children) ?? []
        for index in children.indices {
            let expected = try FilePath.child(children[index].name, in: path)
            guard FilePath.normalized(children[index].path) == expected else { throw APIError.incompatible }
            children[index].path = expected
        }
    }
    var file: RemoteFile {
        RemoteFile(name: name, path: path, type: type, size: size, modified: modified,
                   hasPreview: hasPreview, revision: revision, archiveLink: archiveLink, archiveRole: archiveRole, archiveRoot: archiveRoot, archiveLibrary: archiveLibrary)
    }
}

enum FilePath {
    static func inSource(_ relative: String, scope: String) throws -> String {
        for value in [relative, scope] {
            guard !value.contains("\\"), !value.contains("\0"),
                  !value.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }) else { throw APIError.invalidName }
        }
        return normalized(scope + "/" + relative)
    }
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
        case .incompatible: return "This connection requires the fileBeans filesystem API."
        case .status(let code): return "The server could not complete the request (\(code))."
        }
    }
}

// Formatters and parsed values are reused across sorting, provider metadata and rows.
// The lock confines the mutable Foundation formatters to one caller at a time.
enum FileDates {
    private static let lock = NSLock()
    private static let fractional: ISO8601DateFormatter = {
        let value = ISO8601DateFormatter()
        value.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return value
    }()
    private static let standard = ISO8601DateFormatter()
    private static let cache = NSCache<NSString, NSDate>()
    static func parse(_ value: String) -> Date? {
        lock.lock(); defer { lock.unlock() }
        if let date = cache.object(forKey: value as NSString) { return date as Date }
        guard let date = fractional.date(from: value) ?? standard.date(from: value) else { return nil }
        cache.countLimit = 20000
        cache.setObject(date as NSDate, forKey: value as NSString)
        return date
    }
}
