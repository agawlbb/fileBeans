import Foundation

struct ArchiveEndpoint: Codable, Hashable {
    let source: String
    let path: String
    let available: Bool
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
    var hasPending: Bool { pendingOriginals > 0 || pendingChanges > 0 || problems > 0 }
}
extension CompanionClient {
    func archiveSyncStatus() async throws -> ArchiveSyncStatus {
        try JSONDecoder().decode(ArchiveSyncStatus.self, from: await request("archive-sync/status"))
    }
    func archiveLinks() async throws -> [ArchiveLink] {
        try JSONDecoder().decode([ArchiveLink].self, from: await request("archive-links"))
    }
    func linkArchive(library: StoredItem, archive: StoredItem) async throws -> ArchiveLink {
        let a = try sourceLocation(CopyLocation(source: library.source, path: library.file.path))
        let b = try sourceLocation(CopyLocation(source: archive.source, path: archive.file.path))
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
