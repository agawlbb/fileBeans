import SwiftUI

@MainActor
final class BrowserModel: ObservableObject {
    @Published var items: [StoredItem] = []
    @Published var busy = false
    @Published var error: String?
    @Published var uploadProgress: Double?
    @Published var uploadName = ""
    @Published var canRetry = false
    @Published var canCreateProtectedFolder = false
    @Published var sourceScope = "/"
    @Published var parentProtection: FolderProtection?
    private var transfer: Task<Void, Never>?
    private var pending: [(URL, StoredItem)] = []
    private(set) var repository: Repository?

    func load(connection: Connection, parent: StoredItem?) async {
        busy = true; defer { busy = false }
        do {
            if repository == nil { repository = try Repository(connection: connection, store: SharedStore()) }
            guard let repository else { return }
            if let companion = repository.companion {
                canCreateProtectedFolder = (try? await companion.capabilities().features?.contains("protectedFoldersCreate")) == true
            }
            let transferJobs = try? await repository.companion?.jobs()
            if let parent {
                // Refresh access before each listing, including after token replacement.
                let roots = try await repository.roots()
                guard let root = roots.first(where: { $0.source == parent.source }),
                      FilePath.contains(parent.file.path, in: root.file.path) else { throw APIError.forbidden }
                sourceScope = try repository.store.sourceScope(connection.id, source: parent.source)
                var refreshed = parent; refreshed.permissions = root.permissions
                items = try await repository.children(refreshed)
                parentProtection = repository.protectedItem(refreshed).protection
            } else { items = try await repository.roots() }
            if let jobs = transferJobs {
                FileActivity.shared.synchronize(jobs, connection: connection.id)
            }
            error = nil
        } catch {
            // SwiftUI cancels the listing task when navigating away.
            guard !Task.isCancelled, !(error is CancellationError),
                  (error as? URLError)?.code != .cancelled else { return }
            self.error = error.localizedDescription
            if let parent, let repository {
                parentProtection = repository.protectedItem(parent).protection
                if parentProtection?.unlocked == false { items = [] }
            }
        }
    }
    @discardableResult
    func perform(_ operation: () async throws -> Void) async -> Bool {
        busy = true; defer { busy = false }
        do { try await operation(); return true } catch { self.error = error.localizedDescription; return false }
    }

    func delete(_ values: [StoredItem], connection: Connection, parent: StoredItem?) async {
        guard let repository else { return }
        let activity = FileActivity.shared
        let ids = activity.begin(values.map { ActivityFile(source: $0.source, path: $0.file.path, name: $0.file.name, folder: $0.file.isDirectory) },
                                 connection: connection.id, operation: "delete")
        var failures: [String] = []
        for (index,item) in values.enumerated() {
            do { try await repository.delete(item); items.removeAll { $0.id == item.id } }
            catch { failures.append(item.file.name + ": " + error.localizedDescription) }
            activity.finish([ids[index]])
        }
        if !failures.isEmpty { error = failures.joined(separator: "\n") }
    }
    func transfer(_ values: [StoredItem], to destination: BrowserDestination, copying: Bool) async {
        guard let repository else { return }
        guard !values.isEmpty else { error = APIError.missing.localizedDescription; return }
        let connection = repository.connection
        let target: CopyLocation
        switch destination {
        case .local(let parent): target = CopyLocation(source: parent.source, path: parent.file.path)
        case .drive(let parent): target = CopyLocation(source: "drive", path: parent.id, name: parent.name)
        }
        let activity = FileActivity.shared
        let ids = activity.begin(values.map { ActivityFile(source: $0.source, path: $0.file.path, name: $0.file.name, folder: $0.file.isDirectory) },
            connection: connection.id, operation: copying ? "copy" : "move", destination: target)
        if copying, let companion = repository.companion {
            do {
                guard values.allSatisfy({ !$0.isSource && $0.permissions.download }), !values.isEmpty else { throw APIError.forbidden }
                if case .local(let parent) = destination {
                    guard parent.permissions.create else { throw APIError.forbidden }
                    for item in values {
                        let targetPath = try FilePath.child(item.file.name, in: parent.file.path)
                        guard item.source != parent.source || (!FilePath.contains(parent.file.path, in: item.file.path) && targetPath != item.file.path) else { throw APIError.conflict }
                        try repository.requireUnlocked(item)
                        guard item.protection?.isRoot != true else { throw APIError.forbidden }
                    }
                }
                let job = try await companion.submit(BackgroundCopy(items: values.map { CopyLocation(source: $0.source, path: $0.file.path) }, destination: target))
                activity.attach(job, ids: ids, connection: connection.id)
            } catch { activity.finish(ids); self.error = error.localizedDescription }
            return
        }

        if !copying, case .local(let parent) = destination, let companion = repository.companion {
            do {
                try repository.store.setVaults(await companion.vaults(), connection: connection.id)
                try repository.requireUnlocked(parent)
                let targetProtection = try repository.store.protection(for: parent, connection: connection.id)
                var crossesBoundary = false
                for item in values {
                    try repository.requireUnlocked(item)
                    let protection = try repository.store.protection(for: item, connection: connection.id)
                    guard !item.isSource, item.permissions.modify, parent.permissions.create, protection?.isRoot != true else { throw APIError.forbidden }
                    guard item.source != parent.source || !item.file.isDirectory || !FilePath.contains(parent.file.path, in: item.file.path) else { throw APIError.invalidName }
                    crossesBoundary = crossesBoundary || item.source != parent.source || protection?.id != targetProtection?.id
                }
                if crossesBoundary {
                    let capabilities = try await companion.capabilities()
                    guard capabilities.features?.contains("protectedFolderMoves") == true else { throw APIError.incompatible }
                    let job = try await companion.submit(BackgroundCopy(items: values.map { CopyLocation(source: $0.source, path: $0.file.path) },
                        destination: target, operation: "move"))
                    activity.attach(job, ids: ids, connection: connection.id)
                    return
                }
            } catch { activity.finish(ids); self.error = error.localizedDescription; return }
        }
        var failures: [String] = []
        for (index,item) in values.enumerated() {
            do {
                guard case .local(let parent) = destination else { throw APIError.forbidden }
                if copying { try await repository.copy(item,to:parent) }
                else { try await repository.move(item,to:parent,name:item.file.name) }
                items.removeAll { !copying && $0.id == item.id }
            } catch { failures.append(item.file.name + ": " + error.localizedDescription) }
            activity.finish([ids[index]])
        }
        if !failures.isEmpty { error = failures.joined(separator: "\n") }
    }
    func enqueue(urls: [URL], parent: StoredItem, scoped: Bool = true) {
        do {
            guard let repository else { throw APIError.authentication }
            let directory = repository.store.cacheDirectory(repository.connection.id).appendingPathComponent("uploads")
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            for url in urls {
                let access = scoped && url.startAccessingSecurityScopedResource()
                defer { if access { url.stopAccessingSecurityScopedResource() } }
                let container = directory.appendingPathComponent(UUID().uuidString)
                try FileManager.default.createDirectory(at: container, withIntermediateDirectories: true)
                let copy = container.appendingPathComponent(url.lastPathComponent)
                try FileManager.default.copyItem(at: url, to: copy)
                pending.append((copy, parent))
            }
            startUploads()
        } catch { self.error = error.localizedDescription }
    }
    func retry() { startUploads() }
    func cancel() {
        transfer?.cancel()
        // The active task owns cleanup until cancellation is acknowledged.
    }
    private func startUploads() {
        guard transfer == nil, !pending.isEmpty, let repository else { return }
        canRetry = false
        transfer = Task {
            defer { transfer = nil; uploadProgress = nil }
            while let (url, parent) = pending.first {
                uploadName = url.lastPathComponent; uploadProgress = 0
                do {
                    try Task.checkCancellation()
                    guard parent.permissions.create else { throw APIError.forbidden }
                    let path = try FilePath.child(url.lastPathComponent, in: parent.file.path)
                    if repository.companion != nil && CompanionClient.protected(path) { throw APIError.forbidden }
                    try await repository.client.upload(source: parent.source, path: path, file: url) { value in
                        Task { @MainActor in if self.transfer != nil { self.uploadProgress = value } }
                    }
                    pending.removeFirst()
                    try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
                    items = try await repository.children(parent)
                    repository.notify(parent.id)
                } catch {
                    if Task.isCancelled {
                        for (url, _) in pending { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
                        pending.removeAll(); canRetry = false
                    } else { self.error = error.localizedDescription; canRetry = true }
                    return
                }
            }
        }
    }
    deinit { transfer?.cancel() }
}
