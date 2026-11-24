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
extension CompanionClient {
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
