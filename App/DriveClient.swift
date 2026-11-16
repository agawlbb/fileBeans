import Foundation
import CryptoKit

struct DrivePermissions: Decodable {
    let canDownload: Bool?
    let canCopy: Bool?
    let canRename: Bool?
    let canEdit: Bool?
    let canMoveItemWithinDrive: Bool?
    let canMoveItemOutOfDrive: Bool?
    let canAddChildren: Bool?
    let canTrash: Bool?
    let canUntrash: Bool?
    let canDelete: Bool?
}
struct DriveFile: Decodable, Identifiable {
    let id: String
    let name: String
    let mimeType: String
    let size: String?
    let version: String?
    let modifiedTime: String?
    let parents: [String]?
    let driveId: String?
    let webViewLink: String?
    let hasThumbnail: Bool?
    let trashed: Bool?
    let capabilities: DrivePermissions?
    let sourceRoot: Bool?
    init(id: String, name: String, mimeType: String, size: String? = nil, version: String? = nil,
         modifiedTime: String? = nil, parents: [String]? = nil, driveId: String? = nil,
         webViewLink: String? = nil, hasThumbnail: Bool? = nil, trashed: Bool? = nil,
         capabilities: DrivePermissions? = nil, sourceRoot: Bool? = nil) {
        self.id = id; self.name = name; self.mimeType = mimeType; self.size = size; self.version = version
        self.modifiedTime = modifiedTime; self.parents = parents; self.driveId = driveId
        self.webViewLink = webViewLink; self.hasThumbnail = hasThumbnail; self.trashed = trashed
        self.capabilities = capabilities; self.sourceRoot = sourceRoot
    }
    var folder: Bool { mimeType == "application/vnd.google-apps.folder" }
    var nativeDocument: Bool { mimeType.hasPrefix("application/vnd.google-apps.") && !folder }
    var downloadable: Bool { !folder && !nativeDocument && capabilities?.canDownload == true }
    var canCopy: Bool { sourceRoot != true && (folder || capabilities?.canCopy == true || downloadable) }
    var canMove: Bool { sourceRoot != true && (capabilities?.canMoveItemWithinDrive == true || capabilities?.canMoveItemOutOfDrive == true) }
    var canRename: Bool { sourceRoot != true && capabilities?.canRename == true }
    var canTrash: Bool { sourceRoot != true && capabilities?.canTrash == true }
    var media: Bool { mimeType.hasPrefix("video/") || mimeType.hasPrefix("audio/") }
    var sortValue: BrowserSortValue {
        BrowserSortValue(id: id, name: name, folder: folder, size: size.flatMap(Int64.init) ?? 0,
                         modified: modifiedTime.flatMap { value in
                             let formatter = ISO8601DateFormatter()
                             formatter.formatOptions.insert(.withFractionalSeconds)
                             return formatter.date(from: value) ?? ISO8601DateFormatter().date(from: value)
                         } ?? .distantPast)
    }
}
struct DrivePage: Decodable { let files: [DriveFile]; let nextPageToken: String? }
struct SharedDrive: Decodable, Identifiable { let id: String; let name: String }
struct SharedDrivePage: Decodable { let drives: [SharedDrive]; let nextPageToken: String? }
struct DriveMutationRequest: Encodable {
    let action: String
    var fileId: String? = nil
    var parent: String? = nil
    var name: String? = nil
    var scope: String? = nil
    var version: String? = nil
}
struct DriveUpload: Decodable {
    let id: String
    let name: String
    let parent: String
    let size: Int64
    let received: Int64
    let state: String
    let jobId: String?
}
final class DriveClient {
    let transport: CompanionClient
    init(connection: Connection) { transport = CompanionClient(connection: connection) }
    func close() { transport.close() }
    func list(folder: String, page: String = "") async throws -> DrivePage {
        try JSONDecoder().decode(DrivePage.self, from: await transport.request("drive/files", query: ["folder": folder, "page": page]))
    }
    func file(_ id: String) async throws -> DriveFile {
        try JSONDecoder().decode(DriveFile.self, from: await transport.request("drive/files/" + id))
    }
    func drives(page: String = "") async throws -> SharedDrivePage {
        try JSONDecoder().decode(SharedDrivePage.self, from: await transport.request("drive/drives", query: ["page": page]))
    }
    func trash(scope: String, page: String = "") async throws -> DrivePage {
        try JSONDecoder().decode(DrivePage.self, from: await transport.request("drive/trash", query: ["scope": scope, "page": page]))
    }
    func mutate(_ request: DriveMutationRequest, permanent: Bool = false) async throws {
        let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys
        let body = try encoder.encode(request)
        let identity = Data((transport.connection.id.uuidString + (transport.connection.companionURL?.absoluteString ?? "")).utf8) + body
        let hash = SHA256.hash(data: identity).map { String(format: "%02x", $0) }.joined()
        let directory = try AppConfiguration.directory.appendingPathComponent("drive-operations", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let pending = directory.appendingPathComponent(hash + ".key")
        let key = (try? String(contentsOf: pending, encoding: .utf8)) ?? UUID().uuidString
        try Data(key.utf8).write(to: pending, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        do {
            _ = try await transport.request("drive/mutations", method: "POST", body: body, key: key, permanent: permanent)
            try? FileManager.default.removeItem(at: pending)
        } catch {
            let failure = error as NSError
            if failure.domain == "fileBeans.Companion", [400, 401, 403, 404, 409].contains(failure.code) { try? FileManager.default.removeItem(at: pending) }
            throw error
        }
    }
    func connect() async throws -> URL {
        struct Authorization: Decodable { let url: URL }
        let result = try JSONDecoder().decode(Authorization.self, from: await transport.request("drive/connect", method: "POST"))
        guard result.url.scheme == "https", result.url.host == "accounts.google.com" else { throw APIError.invalidURL }
        return result.url
    }
}
