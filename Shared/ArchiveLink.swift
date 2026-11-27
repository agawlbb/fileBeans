import Foundation

struct ArchiveEndpoint: Codable, Hashable {
    let source: String
    let path: String
    let available: Bool
    var server: String? = nil
}
struct ArchiveLink: Codable, Identifiable, Hashable {
    let id: String
    let library: ArchiveEndpoint
    let archive: ArchiveEndpoint
}
struct ArchiveSyncStatus: Decodable {
    let pendingOriginals: Int
    let pendingChanges: Int
    let problems: Int
    let pendingWork: Int?
    let items: [ArchiveWorkStatus]?
    var hasPending: Bool { pendingOriginals > 0 || pendingChanges > 0 || problems > 0 || (pendingWork ?? 0) > 0 || items?.isEmpty == false }
}
struct ArchiveWorkStatus: Decodable, Identifiable {
    let id: String
    let name: String
    let source: String
    let destination: String
    let phase: String
    let error: String?
    let next: Int64?
    var active: Bool { ["transcoding", "copying", "updating"].contains(phase) }
    var status: String {
        switch phase {
        case "transcoding": return "Transcoding"
        case "copying": return "Copying"
        case "queuedTranscode": return "Queued for transcoding"
        case "queuedCopy": return "Waiting for copy"
        case "retrying": return "Waiting to retry"
        case "waitingMemory": return "Waiting for memory"
        case "finishing": return "Finishing"
        case "attention": return "Needs attention"
        case "updating": return "Updating"
        default: return "Queued"
        }
    }
}
extension CompanionClient {
    func archiveSyncStatus() async throws -> ArchiveSyncStatus {
        try JSONDecoder().decode(ArchiveSyncStatus.self, from: await workRequest("archive-sync/status"))
    }
    func archiveLinks() async throws -> [ArchiveLink] {
        try JSONDecoder().decode([ArchiveLink].self, from: await request("archive-links"))
    }
    func linkArchive(library: StoredItem, archive: StoredItem, archiveConnection: Connection? = nil) async throws -> ArchiveLink {
        let a = try sourceLocation(CopyLocation(source: library.source, path: library.file.path))
        var b = try sourceLocation(CopyLocation(source: archive.source, path: archive.file.path))
        if let archiveConnection, archiveConnection.id != connection.id {
            let client = CompanionClient(connection: archiveConnection); defer { client.close() }
            let capabilities = try await client.capabilities()
            guard let owner = capabilities.serverId, !owner.isEmpty else { throw APIError.incompatible }
            let scope = try SharedStore().sourceScope(archiveConnection.id, source: archive.source)
            b = CopyLocation(source: archive.source, path: try FilePath.inSource(archive.file.path, scope: scope), server: owner)
        }
        let body = try JSONEncoder().encode(["library": a, "archive": b])
        return try JSONDecoder().decode(ArchiveLink.self, from: await request("archive-links", method: "POST", body: body))
    }
    func unlinkArchive(_ id: String) async throws {
        _ = try await request("archive-links/\(id)", method: "DELETE")
    }
}

extension RemoteFile {
    var archiveWriteMessage: String? {
        guard archiveRole == "archive" else { return nil }
        if let library = archiveLibrary {
            return "Make changes in \(library.source) \(library.path)."
        }
        return "Make changes in the matched folder."
    }
    func requireArchiveWritable() throws {
        if let message = archiveWriteMessage {
            throw NSError(domain: "fileBeans.ArchiveReadOnly", code: 403,
                          userInfo: [NSLocalizedDescriptionKey: message])
        }
    }
}
