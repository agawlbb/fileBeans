import Foundation
import FileProvider

final class Repository {
    let connection: Connection
    let client: FilesystemClient
    let companion: CompanionClient?
    let store: SharedStore
    let fileProvider: Bool
    private var protectionAvailable = false
    init(connection: Connection, store: SharedStore, token: String? = nil, fileProvider: Bool = false) throws {
        let connection = connection.unified
        self.fileProvider = fileProvider
        self.connection = connection; self.store = store
        client = FilesystemClient(connection: connection, token: token)
        companion = CompanionClient(connection: connection, credential: token, store: store)
    }
    deinit { client.close(); companion?.close() }
    func refreshProtection() async {
        guard let companion else { return }
        do {
            try store.setVaults(await companion.vaults(), connection: connection.id)
            protectionAvailable = true
        }
        catch {
            protectionAvailable = false
            if let known = try? store.vaults(connection.id), !known.isEmpty {
                try? store.setVaults(known.map { var v = $0; v.state = "unknown"; return v }, connection: connection.id)
            }
        }
    }
    func requireProviderAccess(_ item: StoredItem) throws {
        if fileProvider && protectedItem(item).protection?.unlocked == false { throw APIError.missing }
    }
    func requireUnlocked(_ item: StoredItem) throws {
        if let protection = protectedItem(item).protection, !protection.unlocked {
            throw NSError(domain: "fileBeans.VaultLocked", code: 409,
                          userInfo: [NSLocalizedDescriptionKey: "Unlock this folder in fileBeans."])
        }
    }
    func roots() async throws -> [StoredItem] {
        let scopes = try await client.scopes().filter { $0.permissions.view }
        var result: [StoredItem] = []
        for scope in scopes {
            try store.updateScope(connection: connection.id, source: scope.name, boundary: scope.scope)
            // All resource routes take paths relative to the authenticated user's source scope.
            let file = RemoteFile(name: scope.name, path: "/", type: "directory")
            result += try store.reconcile(connection: connection.id, parent: "root", source: scope.name,
                                          files: [file], permissions: scope.permissions, sourceRoots: true)
        }
        // Remove sources no longer authorized, including their cached descendants.
        let allowed = Set(scopes.map(\.name))
        try store.transaction {
            let key = connection.id.uuidString
            let before = $0.items[key] ?? [:]
            $0.items[key] = before.filter { allowed.contains($0.value.source) }
            if $0.items[key] != before {
                $0.revisions[key, default: 0] += 1
                for (id, item) in before where $0.items[key]?[id] == nil {
                    $0.deleted[key, default: [:]][id + ":" + item.parent] = Tombstone(id: id, parent: item.parent, revision: $0.revisions[key]!)
                }
            }
        }
        await refreshProtection()
        if fileProvider && !protectionAvailable { throw APIError.forbidden }
        return try protectedItems(result).filter { !fileProvider || $0.protection?.unlocked != false }
    }
    func children(_ parent: StoredItem?) async throws -> [StoredItem] {
        guard let parent else { return try await roots() }
        _ = try item(parent.id)
        await refreshProtection()
        if fileProvider && !protectionAvailable { throw APIError.forbidden }
        try requireProviderAccess(parent)
        try requireUnlocked(parent)
        let resource = try await client.resource(source: parent.source, path: parent.file.path)
        let vaults = try store.vaults(connection.id)
        let scope = try store.sourceScope(connection.id, source: parent.source)
        let files = resource.children.filter { file in
            guard companion != nil else { return true }
            let candidate = StoredItem(id: "", parent: parent.id, source: parent.source, file: file, permissions: parent.permissions)
            let protection = SharedStore.protection(for: candidate, vaults: vaults, scope: scope)
            if protection?.unlocked == false { return false }
            return !SharedStore.isSystemEntry(candidate, vaults: vaults, scope: scope)
        }
        var permissions = parent.permissions
        if companion != nil && CompanionClient.protected(parent.file.path) {
            permissions.create = false; permissions.modify = false; permissions.delete = false
        }
        let children = try store.reconcile(connection: connection.id, parent: parent.id, source: parent.source,
                                           files: files, permissions: permissions)
        return try protectedItems(children).filter { !fileProvider || $0.protection?.unlocked != false }
    }
    // A listing must read the catalog once, rather than decode its entire JSON
    // once per row. The server protection snapshot is refreshed before this call.
    func protectedItems(_ values: [StoredItem]) throws -> [StoredItem] {
        guard companion != nil else { return values }
        return try store.transaction { catalog in
            let key = connection.id.uuidString
            let vaults = catalog.protectedFolders?[key] ?? []
            return values.map { value in
                var item = value
                item.protection = SharedStore.protection(for: item, vaults: vaults, scope: catalog.boundaries[key]?[item.source] ?? "/")
                if CompanionClient.protected(item.file.path) {
                    item.permissions.create = false; item.permissions.modify = false; item.permissions.delete = false
                }
                return item
            }
        }
    }
    func protectedItem(_ value: StoredItem) -> StoredItem {
        var item = value
        if companion != nil {
            do { item.protection = try store.protection(for: item, connection: connection.id) }
            catch { if var protection = item.protection { protection.state = "unknown"; item.protection = protection } }
        }
        if companion != nil && CompanionClient.protected(item.file.path) {
            item.permissions.create = false; item.permissions.modify = false; item.permissions.delete = false
        }
        return item
    }
    func item(_ id: String) throws -> StoredItem {
        guard let item = try store.items(connection.id)[id] else { throw APIError.missing }
        try requireProviderAccess(item)
        return protectedItem(item)
    }
    func refreshKnownFolders() async throws {
        _ = try await roots()
        let ids = try store.transaction { $0.enumerated[connection.id.uuidString] ?? [] }
        for id in ids {
            if let parent = try store.items(connection.id)[id], parent.protection?.unlocked != false, !fileProvider || protectedItem(parent).protection?.unlocked != false { _ = try await children(parent) }
        }
    }
    func notify(_ parent: String = "root") {
        let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(connection.id.uuidString), displayName: connection.name)
        let manager = NSFileProviderManager(for: domain)
        manager?.signalEnumerator(for: parent == "root" ? .rootContainer : NSFileProviderItemIdentifier(parent)) { _ in }
        manager?.signalEnumerator(for: .workingSet) { _ in }
    }
    func move(_ item: StoredItem, to parent: StoredItem, name: String) async throws {
        try requireProviderAccess(item)
        try requireProviderAccess(parent)
        var target = parent
        target.file.path = try FilePath.child(name, in: parent.file.path)
        try requireProviderAccess(target)
        guard !item.isSource, (item.source == parent.source || companion != nil), item.permissions.modify,
              (item.parent == parent.id || parent.permissions.create) else { throw APIError.forbidden }
        let path = try FilePath.child(name, in: parent.file.path)
        if companion != nil && (CompanionClient.protected(item.file.path) || CompanionClient.protected(path)) { throw APIError.forbidden }
        guard item.source != parent.source || !item.file.isDirectory || !FilePath.contains(parent.file.path, in: item.file.path) else { throw APIError.invalidName }
        if item.source == parent.source && path == item.file.path { return }
        var serverMove = false
        if let companion {
            try store.setVaults(await companion.vaults(), connection: connection.id)
            try requireUnlocked(item); try requireUnlocked(parent)
            let origin = try store.protection(for: item, connection: connection.id)
            let target = try store.protection(for: parent, connection: connection.id)
            if origin?.isRoot == true {
                guard item.source == parent.source, target == nil else { throw APIError.forbidden }
                try await companion.relocateProtectedFolder(protectedItem(item), parent: parent, name: name)
                try store.relocate(item, connection: connection.id, path: path, parent: parent.id, source: parent.source, permissions: parent.permissions)
                await refreshProtection()
                notify(item.parent); notify(parent.id)
                return
            }
            serverMove = item.source != parent.source || origin?.id != target?.id
            if serverMove {
                let capabilities = try await companion.capabilities()
                guard capabilities.features?.contains("protectedFolderMoves") == true else { throw APIError.incompatible }
                let job = try await companion.submit(BackgroundCopy(
                    items: [CopyLocation(source: item.source, path: item.file.path)],
                    destination: CopyLocation(source: parent.source, path: parent.file.path),
                    operation: "move", destinationName: name))
                try await companion.waitForMove(job)
            }
        }
        if !serverMove { try await client.move(source: item.source, from: item.file.path, to: path) }
        try store.relocate(item, connection: connection.id, path: path, parent: parent.id, source: parent.source, permissions: parent.permissions)
        if item.source != parent.source { _ = try await children(try self.item(item.parent)) }
        _ = try await children(parent)
        notify(item.parent); notify(parent.id)
    }
    func copy(_ item: StoredItem, to parent: StoredItem) async throws {
        try await copy([item], to: parent)
    }
    func copy(_ items: [StoredItem], to parent: StoredItem) async throws {
        guard !items.isEmpty, parent.file.isDirectory, parent.permissions.create else { throw APIError.forbidden }
        for item in items {
            guard !item.isSource, item.permissions.download else { throw APIError.forbidden }
            let path = try FilePath.child(item.file.name, in: parent.file.path)
            guard item.source != parent.source || path != item.file.path else { throw APIError.conflict }
            guard item.source != parent.source || !item.file.isDirectory || !FilePath.contains(parent.file.path, in: item.file.path) else { throw APIError.invalidName }
            if companion != nil && (CompanionClient.protected(item.file.path) || CompanionClient.protected(path)) { throw APIError.forbidden }
        }
        if let companion {
            _ = try await companion.submit(BackgroundCopy(items: items.map { CopyLocation(source: $0.source, path: $0.file.path) },
                                                         destination: CopyLocation(source: parent.source, path: parent.file.path)))
            return
        }
        throw APIError.incompatible
    }
    func delete(_ item: StoredItem) async throws {
        guard !item.isSource, item.permissions.delete else { throw APIError.forbidden }
        if let companion {
            guard !CompanionClient.protected(item.file.path) else { throw APIError.forbidden }
            try await companion.trash(item)
        } else {
            throw APIError.incompatible
        }
        _ = try await children(try self.item(item.parent))
        notify(item.parent)
    }
    func createFolder(name: String, parent: StoredItem) async throws {
        guard parent.permissions.create else { throw APIError.forbidden }
        let path = try FilePath.child(name, in: parent.file.path)
        if companion != nil && CompanionClient.protected(path) { throw APIError.forbidden }
        try await client.createFolder(source: parent.source, path: path)
        _ = try await children(parent); notify(parent.id)
    }
}
