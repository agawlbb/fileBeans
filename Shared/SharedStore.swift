import Foundation
import Security
import Darwin

enum AppConfiguration {
    static var group: String { Bundle.main.object(forInfoDictionaryKey: "AppGroup") as! String }
    static var keychain: String { Bundle.main.object(forInfoDictionaryKey: "KeychainGroup") as! String }
    static var directory: URL {
        get throws {
            guard let url = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else {
                throw CocoaError(.fileNoSuchFile)
            }
            return url
        }
    }
}

enum TokenStore {
    private static func query(_ id: UUID) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "myFiles.Quantum",
         kSecAttrAccount as String: id.uuidString, kSecAttrAccessGroup as String: AppConfiguration.keychain]
    }
    static func read(_ id: UUID) throws -> String {
        var q = query(id)
        q[kSecReturnData as String] = true
        var value: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &value)
        guard status == errSecSuccess, let d = value as? Data, let token = String(data: d, encoding: .utf8) else {
            throw APIError.authentication
        }
        return token
    }
    static func save(_ token: String, for id: UUID) throws {
        var q = query(id)
        let attrs: [String: Any] = [kSecValueData as String: Data(token.utf8),
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
        var status = SecItemUpdate(q as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound {
            q.merge(attrs) { _, new in new }
            status = SecItemAdd(q as CFDictionary, nil)
        }
        guard status == errSecSuccess else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(status)) }
    }
    static func remove(_ id: UUID) throws {
        let status = SecItemDelete(query(id) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}

struct StoredItem: Codable, Identifiable, Hashable {
    var id: String
    var parent: String
    var source: String
    var file: RemoteFile
    var permissions: Permissions
    var isSource: Bool = false
    var protection: FolderProtection? = nil
}

struct Catalog: Codable, Equatable {
    var protectedFolders: [String: [ProtectedVault]]? = nil
    var connections: [Connection] = []
    var items: [String: [String: StoredItem]] = [:]
    var enumerated: [String: Set<String>] = [:]
    var revisions: [String: Int64] = [:]
    var deleted: [String: [String: Tombstone]] = [:]
    var boundaries: [String: [String: String]] = [:]
}

struct Tombstone: Codable, Equatable {
    var id: String
    var parent: String
    var revision: Int64
}

/// File coordination protects transactions from suspension; the POSIX lock also
/// serializes access with an older app/extension during an update.
final class SharedStore {
    let directory: URL
    init(directory: URL) { self.directory = directory }
    convenience init() throws { self.init(directory: try AppConfiguration.directory) }
    func transaction<T>(_ body: (inout Catalog) throws -> T) throws -> T {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        // Coordinate a stable sidecar, since catalog.json is atomically replaced.
        // iOS grants background execution time before the accessor runs, and
        // cancels requests still waiting when the process is suspended. Keep
        // acquisition, all catalog I/O, and lock release inside that accessor.
        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var outcome: Result<T, Error>?
        coordinator.coordinate(writingItemAt: directory.appendingPathComponent("catalog.lock"),
                               options: [], error: &coordinationError) { _ in
            outcome = Result { try lockedTransaction(body) }
        }
        if let outcome { return try outcome.get() }
        if let coordinationError { throw coordinationError }
        throw CocoaError(.fileLocking)
    }
    private func lockedTransaction<T>(_ body: (inout Catalog) throws -> T) throws -> T {
        let fd = open(directory.appendingPathComponent("catalog.lock").path, O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
        guard fd >= 0 else { throw CocoaError(.fileWriteUnknown) }
        defer { close(fd) }
        guard flock(fd, LOCK_EX) == 0 else { throw CocoaError(.fileLocking) }
        defer { flock(fd, LOCK_UN) }
        let url = directory.appendingPathComponent("catalog.json")
        var state = Catalog()
        if FileManager.default.fileExists(atPath: url.path) {
            state = try JSONDecoder().decode(Catalog.self, from: Data(contentsOf: url))
        }
        let original = state
        let result = try body(&state)
        if state != original || !FileManager.default.fileExists(atPath: url.path) {
            try JSONEncoder().encode(state).write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        }
        return result
    }
    func connections() throws -> [Connection] { try transaction { $0.connections.map(\.unified) } }
    func save(_ connection: Connection) throws {
        try transaction { state in
            state.connections.removeAll { $0.id == connection.id }
            state.connections.append(connection)
        }
    }
    func remove(_ id: UUID) throws {
        try transaction {
            $0.connections.removeAll { $0.id == id }
            $0.items.removeValue(forKey: id.uuidString)
            $0.enumerated.removeValue(forKey: id.uuidString)
            $0.revisions.removeValue(forKey: id.uuidString)
            $0.deleted.removeValue(forKey: id.uuidString)
            $0.boundaries.removeValue(forKey: id.uuidString)
        }
        let cache = cacheDirectory(id)
        if FileManager.default.fileExists(atPath: cache.path) { try FileManager.default.removeItem(at: cache) }
    }
    func cacheDirectory(_ id: UUID) -> URL {
        directory.appendingPathComponent("Library/Caches").appendingPathComponent(id.uuidString)
    }
    func items(_ id: UUID) throws -> [String: StoredItem] { try transaction { $0.items[id.uuidString] ?? [:] } }
    func sourceScope(_ id: UUID, source: String) throws -> String {
        try transaction { $0.boundaries[id.uuidString]?[source] ?? "/" }
    }
    func revision(_ id: UUID) throws -> Int64 { try transaction { $0.revisions[id.uuidString] ?? 0 } }
    func updateScope(connection: UUID, source: String, boundary: String) throws {
        try transaction { state in
            let key = connection.uuidString
            if let old = state.boundaries[key]?[source], old != boundary {
                state.revisions[key, default: 0] += 1
                let removed = (state.items[key] ?? [:]).values.filter { $0.source == source }
                for item in removed {
                    state.items[key]?.removeValue(forKey: item.id)
                    state.enumerated[key]?.remove(item.id)
                    state.deleted[key, default: [:]][item.id + ":" + item.parent] = Tombstone(
                        id: item.id, parent: item.parent, revision: state.revisions[key]!)
                }
            }
            state.boundaries[key, default: [:]][source] = boundary
        }
    }
    func reconcile(connection: UUID, parent: String, source: String, files: [RemoteFile], permissions: Permissions,
                   sourceRoots: Bool = false) throws -> [StoredItem] {
        try transaction { state in
            let key = connection.uuidString
            var items = state.items[key] ?? [:]
            let old = items
            var result: [StoredItem] = []
            for file in files {
                let existing = items.values.first { $0.source == source && $0.file.path == file.path && $0.isSource == sourceRoots }
                var item = StoredItem(id: existing?.id ?? UUID().uuidString, parent: parent, source: source,
                                      file: file, permissions: permissions, isSource: sourceRoots)
                item.protection = Self.protection(for: item, vaults: state.protectedFolders?[key] ?? [],
                    scope: state.boundaries[key]?[source] ?? "/")
                items[item.id] = item
                result.append(item)
                if sourceRoots {
                    for (id, var child) in items where child.source == source {
                        child.permissions = permissions
                        items[id] = child
                    }
                }
            }
            let live = Set(result.map(\.id))
            let removed = items.values.filter { $0.parent == parent && $0.source == source && $0.protection?.unlocked != false && !live.contains($0.id) }
            for item in removed {
                items = items.filter {
                    live.contains($0.key) || ($0.key != item.id && !($0.value.source == item.source && FilePath.contains($0.value.file.path, in: item.file.path)))
                }
            }
            state.items[key] = items
            if !sourceRoots { state.enumerated[key, default: []].insert(parent) }
            if old != items {
                state.revisions[key, default: 0] += 1
                for (id, item) in old where items[id] == nil {
                    state.deleted[key, default: [:]][id + ":" + item.parent] = Tombstone(id: id, parent: item.parent, revision: state.revisions[key]!)
                }
            }
            return result
        }
    }
    func relocate(_ item: StoredItem, connection: UUID, path: String, parent: String, source: String? = nil, permissions: Permissions? = nil) throws {
        try transaction { state in
            let key = connection.uuidString
            var items = state.items[key] ?? [:]
            for (id, var child) in items where child.source == item.source && FilePath.contains(child.file.path, in: item.file.path) {
                let suffix = String(child.file.path.dropFirst(item.file.path.count))
                child.file.path = path + suffix
                child.source = source ?? item.source
                child.permissions = permissions ?? child.permissions
                child.protection = Self.protection(for: child, vaults: state.protectedFolders?[key] ?? [],
                    scope: state.boundaries[key]?[child.source] ?? "/")
                if id == item.id { child.parent = parent; child.file.name = (path as NSString).lastPathComponent }
                items[id] = child
            }
            state.items[key] = items
            state.revisions[key, default: 0] += 1
            if item.parent != parent {
                state.deleted[key, default: [:]][item.id + ":" + item.parent] = Tombstone(
                    id: item.id, parent: item.parent, revision: state.revisions[key]!)
            }
        }
    }
}
