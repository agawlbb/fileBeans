import Foundation

struct ProtectedVault: Codable, Identifiable, Hashable {
    let id: String
    let source: String
    var state: String
    let initialized: Bool
    var path: String? = nil
    var name: String? = nil
    var displayName: String { name ?? path.map { ($0 as NSString).lastPathComponent } ?? source }
    func contains(source: String, path: String) -> Bool {
        self.source == source && FilePath.contains(path, in: self.path ?? "/")
    }
}

struct FolderProtection: Codable, Hashable {
    let id: String
    var state: String
    let isRoot: Bool
    var unlocked: Bool { state == "unlocked" }
    var symbol: String { unlocked ? "lock.open.fill" : "lock.fill" }
    var accessibility: String { unlocked ? "Protected, unlocked" : state == "locked" ? "Protected, locked" : "Protected, unavailable" }
}

extension CompanionClient {
    func vaults() async throws -> [ProtectedVault] {
        try JSONDecoder().decode([ProtectedVault].self, from: await request("vaults"))
    }
    func createProtectedFolder(source: String, parent: String, name: String, code: String, requestID: String) async throws -> ProtectedVault {
        let data = try JSONEncoder().encode(["source": source, "parent": parent, "name": name, "key": code])
        return try JSONDecoder().decode(ProtectedVault.self, from: await request("vaults", method: "POST", body: data, key: requestID))
    }
    func vaultAction(id: String, action: String, code: String? = nil) async throws {
        let data = try code.map { try JSONEncoder().encode(["key": $0]) }
        _ = try await request("vaults/\(id)/\(action)", method: "POST", body: data, permanent: action == "initialize")
    }
}

extension SharedStore {
    func vaults(_ connection: UUID) throws -> [ProtectedVault] {
        try transaction { $0.protectedFolders?[connection.uuidString] ?? [] }
    }
    func setVaults(_ vaults: [ProtectedVault], connection: UUID) throws {
        try transaction { catalog in
            let previous = catalog.items[connection.uuidString]
            if catalog.protectedFolders == nil { catalog.protectedFolders = [:] }
            catalog.protectedFolders?[connection.uuidString] = vaults
            for (id, var item) in catalog.items[connection.uuidString] ?? [:] {
                item.protection = Self.protection(for: item, vaults: vaults,
                    scope: catalog.boundaries[connection.uuidString]?[item.source] ?? "/")
                catalog.items[connection.uuidString]?[id] = item
            }
            if previous != catalog.items[connection.uuidString] { catalog.revisions[connection.uuidString, default: 0] += 1 }
        }
    }
    func protection(for item: StoredItem, connection: UUID) throws -> FolderProtection? {
        try transaction { catalog in
            Self.protection(for: item, vaults: catalog.protectedFolders?[connection.uuidString] ?? [],
                scope: catalog.boundaries[connection.uuidString]?[item.source] ?? "/")
        }
    }
    static func protection(for item: StoredItem, vaults: [ProtectedVault], scope: String = "/") -> FolderProtection? {
        guard let path = try? FilePath.inSource(item.file.path, scope: scope),
              let vault = vaults.filter({ $0.contains(source: item.source, path: path) })
            .max(by: { ($0.path?.count ?? 1) < ($1.path?.count ?? 1) }) else { return nil }
        return FolderProtection(id: vault.id, state: vault.state,
            isRoot: path == FilePath.normalized(vault.path ?? "/"))
    }
}
