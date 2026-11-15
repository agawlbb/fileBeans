import Foundation
import FileProvider

final class Repository {
    let connection: Connection
    let client: QuantumClient
    let store: SharedStore
    init(connection: Connection, store: SharedStore, token: String? = nil) throws {
        self.connection = connection; self.store = store
        client = QuantumClient(connection: connection, token: token)
    }
    deinit { client.close() }
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
        return result
    }
    func children(_ parent: StoredItem?) async throws -> [StoredItem] {
        guard let parent else { return try await roots() }
        _ = try item(parent.id)
        let resource = try await client.resource(source: parent.source, path: parent.file.path)
        return try store.reconcile(connection: connection.id, parent: parent.id, source: parent.source,
                                   files: resource.children, permissions: parent.permissions)
    }
    func item(_ id: String) throws -> StoredItem {
        guard let item = try store.items(connection.id)[id] else { throw APIError.missing }
        return item
    }
    func refreshKnownFolders() async throws {
        _ = try await roots()
        let ids = try store.transaction { $0.enumerated[connection.id.uuidString] ?? [] }
        for id in ids {
            if let parent = try store.items(connection.id)[id] { _ = try await children(parent) }
        }
    }
    func notify(_ parent: String = "root") {
        let domain = NSFileProviderDomain(identifier: NSFileProviderDomainIdentifier(connection.id.uuidString), displayName: connection.name)
        let manager = NSFileProviderManager(for: domain)
        manager?.signalEnumerator(for: parent == "root" ? .rootContainer : NSFileProviderItemIdentifier(parent)) { _ in }
        manager?.signalEnumerator(for: .workingSet) { _ in }
    }
    func move(_ item: StoredItem, to parent: StoredItem, name: String) async throws {
        guard !item.isSource, item.source == parent.source, item.permissions.modify,
              (item.parent == parent.id || parent.permissions.create) else { throw APIError.forbidden }
        let path = try FilePath.child(name, in: parent.file.path)
        guard !item.file.isDirectory || !FilePath.contains(parent.file.path, in: item.file.path) else { throw APIError.invalidName }
        if path == item.file.path { return }
        try await client.move(source: item.source, from: item.file.path, to: path)
        try store.relocate(item, connection: connection.id, path: path, parent: parent.id)
        _ = try await children(parent)
        notify(item.parent); notify(parent.id)
    }
    func delete(_ item: StoredItem) async throws {
        guard !item.isSource, item.permissions.delete else { throw APIError.forbidden }
        try await client.delete(source: item.source, path: item.file.path)
        _ = try await children(try self.item(item.parent))
        notify(item.parent)
    }
    func createFolder(name: String, parent: StoredItem) async throws {
        guard parent.permissions.create else { throw APIError.forbidden }
        try await client.createFolder(source: parent.source, path: FilePath.child(name, in: parent.file.path))
        _ = try await children(parent); notify(parent.id)
    }
}
