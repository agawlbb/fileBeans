import Foundation
import FileProvider

struct ProtectedVault: Codable, Identifiable, Hashable {
    let id: String
    let source: String
    var state: String
    let initialized: Bool
    var trashed: Int64? = nil
    var path: String? = nil
    var name: String? = nil
    var displayName: String { name ?? path.map { ($0 as NSString).lastPathComponent } ?? source }
    func contains(source: String, path: String) -> Bool {
        guard let boundary = self.path, !boundary.isEmpty else { return false }
        return (trashed ?? 0) == 0 && self.source == source && FilePath.contains(path, in: boundary)
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
    func relocateProtectedFolder(_ item: StoredItem, parent: StoredItem, name: String) async throws {
        guard let protection = item.protection, protection.isRoot else { throw APIError.forbidden }
        let destination = try sourceLocation(CopyLocation(source: parent.source, path: FilePath.child(name, in: parent.file.path)))
        _ = try await request("vaults/\(protection.id)/relocate", method: "POST", body: JSONEncoder().encode(["path": destination.path]))
    }
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
    func markProtectionLocked(connection: UUID) throws {
        let known = try vaults(connection).map { value -> ProtectedVault in
            var locked = value; locked.state = "locked"; return locked
        }
        try setVaults(known, connection: connection)
    }
    func vaults(_ connection: UUID) throws -> [ProtectedVault] {
        try transaction { $0.protectedFolders?[connection.uuidString] ?? [] }
    }
    func setVaults(_ vaults: [ProtectedVault], connection: UUID) throws {
        let changes: [(StoredItem, Bool)] = try transaction { catalog in
            let previous = catalog.items[connection.uuidString]
            if catalog.protectedFolders == nil { catalog.protectedFolders = [:] }
            // The server deliberately withholds locked locations. Keep existing
            // device-local identity mappings solely to hide cached locked rows.
            let prior = catalog.protectedFolders?[connection.uuidString] ?? []
            let vaults = vaults.map { incoming -> ProtectedVault in
                var value = incoming
                if (value.path ?? "").isEmpty, let known = prior.first(where: { $0.id == value.id }) {
                    value.path = known.path; value.name = known.name
                }
                return value
            }
            catalog.protectedFolders?[connection.uuidString] = vaults
            for (id, var item) in catalog.items[connection.uuidString] ?? [:] {
                item.protection = Self.protection(for: item, vaults: vaults,
                    scope: catalog.boundaries[connection.uuidString]?[item.source] ?? "/")
                catalog.items[connection.uuidString]?[id] = item
                let scope = catalog.boundaries[connection.uuidString]?[item.source] ?? "/"
                if Self.isSystemEntry(item, vaults: vaults, scope: scope) {
                    catalog.items[connection.uuidString]?.removeValue(forKey: id)
                    catalog.deleted[connection.uuidString, default: [:]][id + ":" + item.parent] = Tombstone(
                        id: id, parent: item.parent, revision: catalog.revisions[connection.uuidString, default: 0] + 1)
                }
            }
            let hidden = (catalog.items[connection.uuidString] ?? [:]).values.filter { $0.protection?.unlocked == false && previous?[$0.id]?.protection?.unlocked != false }
            if !hidden.isEmpty {
                catalog.revisions[connection.uuidString, default: 0] += 1
                for item in hidden {
                    catalog.deleted[connection.uuidString, default: [:]][item.id + ":" + item.parent] = Tombstone(
                        id: item.id, parent: item.parent, revision: catalog.revisions[connection.uuidString]!)
                }
            }
            if previous != catalog.items[connection.uuidString] { catalog.revisions[connection.uuidString, default: 0] += 1 }
            return (catalog.items[connection.uuidString] ?? [:]).values.compactMap { item in
                guard item.protection != previous?[item.id]?.protection else { return nil }
                return (item, item.protection?.unlocked == false)
            }
        }
        guard !changes.isEmpty, let configured = try connections().first(where: { $0.id == connection }) else { return }
        let domain = NSFileProviderDomain(identifier: .init(connection.uuidString), displayName: configured.name)
        guard let manager = NSFileProviderManager(for: domain) else { return }
        for parent in Set(changes.map { $0.0.parent }) {
            manager.signalEnumerator(for: parent == "root" ? .rootContainer : .init(parent)) { _ in }
        }
        manager.signalEnumerator(for: .workingSet) { _ in }
        for (item, locked) in changes where locked && !item.file.isDirectory {
            // Best effort: Apple may retain open files or unsynced edits.
            manager.evictItem(identifier: .init(item.id)) { _ in }
        }
    }
    func protection(for item: StoredItem, connection: UUID) throws -> FolderProtection? {
        try transaction { catalog in
            Self.protection(for: item, vaults: catalog.protectedFolders?[connection.uuidString] ?? [],
                scope: catalog.boundaries[connection.uuidString]?[item.source] ?? "/")
        }
    }

    static func isSystemEntry(_ item: StoredItem, vaults: [ProtectedVault], scope: String) -> Bool {
        guard let canonical = try? FilePath.inSource(item.file.path, scope: scope) else { return true }
        if FilePath.contains(canonical, in: "/Trash") { return true }
        if canonical.split(separator: "/").contains(where: { [".filebeans-stage", ".filebeans-vaults", ".filebeans-thumbnails"].contains($0.lowercased()) }) { return true }
        return vaults.contains { $0.source == item.source && FilePath.contains(canonical, in: FilePath.normalized(($0.path ?? "/") + "/Trash")) }
    }
    static func protection(for item: StoredItem, vaults: [ProtectedVault], scope: String = "/") -> FolderProtection? {
        guard let path = try? FilePath.inSource(item.file.path, scope: scope),
              let vault = vaults.filter({ $0.contains(source: item.source, path: path) })
            .max(by: { ($0.path?.count ?? 1) < ($1.path?.count ?? 1) }) else { return nil }
        return FolderProtection(id: vault.id, state: vault.state,
            isRoot: path == FilePath.normalized(vault.path ?? "/"))
    }
}
