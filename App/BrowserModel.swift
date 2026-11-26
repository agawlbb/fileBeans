import SwiftUI

@MainActor
final class BrowserModel: ObservableObject {
    @Published var items: [StoredItem] = []
    @Published var busy = false
    @Published var error: String?
    @Published var canCreateProtectedFolder = false
    @Published var canHost = false
    @Published var canLinkArchive = false
    @Published var sourceScope = "/"
    @Published var parentProtection: FolderProtection?
    private(set) var repository: Repository?

    func load(connection: Connection, parent: StoredItem?) async {
        busy = true; defer { busy = false }
        do {
            if repository == nil {
                repository = try Repository(connection: connection, store: SharedStore())
                repository?.companion?.prepareForJobRetry = { try await ServerRequestRecovery.waitUntilActive() }
            }
            guard let repository else { return }
            if let companion = repository.companion {
                canCreateProtectedFolder = false
                let capabilities = try? await companion.capabilities()
                canLinkArchive = capabilities?.features?.contains("archiveLinksV1") == true
                canHost = capabilities?.features?.contains("publicHostingV1") == true
                let protection = try? JSONDecoder().decode(ProtectionStatus.self, from: await companion.request("protection"))
                if protection?.state == "unlocked" {
                    canCreateProtectedFolder = (try? await companion.capabilities().features?.contains("protectedFoldersCreate")) == true
                }
            }
            let transferJobs = try? await repository.companion?.jobs()
            if let parent {
                // Refresh access before each listing, including after token replacement.
                let roots = try await repository.roots()
                guard let root = roots.first(where: { $0.source == parent.source }),
                      FilePath.contains(parent.file.path, in: root.file.path) else { throw APIError.forbidden }
                sourceScope = try repository.store.sourceScope(connection.id, source: parent.source)
                var refreshed = parent; refreshed.permissions = root.permissions
                let refreshedItems = try await repository.children(refreshed)
                if items != refreshedItems { items = refreshedItems }
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
        do {
            if !copying { for item in values { try repository.requireArchiveWritable(item) } }
            if case .local(let parent) = destination { try repository.requireArchiveWritable(parent) }
        } catch { self.error = error.localizedDescription; return }
        let connection = repository.connection
        if values.contains(where: { $0.protection?.isRoot == true }) {
            for item in values {
                if item.protection?.isRoot != true { await transfer([item], to: destination, copying: copying); continue }
                let ids = FileActivity.shared.begin([ActivityFile(source:item.source,path:item.file.path,name:item.file.name,folder:true)],
                    connection:connection.id,operation:copying ? "copy" : "move",
                    destination: { if case .local(let parent) = destination { return CopyLocation(source:parent.source,path:parent.file.path) }; return nil }())
                do {
                    if let job = try await transferProtectedFolder(item,to:destination,copying:copying) {
                        FileActivity.shared.attach(job,ids:ids,connection:connection.id)
                    } else { FileActivity.shared.finish(ids) }
                    if !copying { items.removeAll { $0.id == item.id } }
                } catch { FileActivity.shared.finish(ids); self.error = error.localizedDescription }
            }
            return
        }
        let target: CopyLocation
        switch destination {
        case .local(let parent): target = CopyLocation(source: parent.source, path: parent.file.path)
        case .drive(let parent): target = CopyLocation(source: "drive", path: parent.id, name: parent.name)
        }
        let activity = FileActivity.shared
        let ids = activity.begin(values.map { ActivityFile(source: $0.source, path: $0.file.path, name: $0.file.name, folder: $0.file.isDirectory) },
            connection: connection.id, operation: copying ? "copy" : "move", destination: target)
        if !copying, case .drive = destination, let companion = repository.companion {
            do {
                guard values.allSatisfy({ !$0.isSource && $0.permissions.download && $0.permissions.delete }) else { throw APIError.forbidden }
                guard try await companion.capabilities().features?.contains("crossDriveMoves") == true else { throw APIError.incompatible }
                let job = try await companion.submit(BackgroundCopy(items: values.map { CopyLocation(source: $0.source, path: $0.file.path) },
                    destination: target, operation: "move"))
                activity.attach(job, ids: ids, connection: connection.id)
            } catch { activity.finish(ids); self.error = error.localizedDescription }
            return
        }
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

    private func transferProtectedFolder(_ item: StoredItem, to destination: BrowserDestination, copying: Bool) async throws -> CopyJob? {
        guard let repository, let companion = repository.companion else { throw APIError.forbidden }
        try repository.requireUnlocked(item)
        if !copying, case .local(let parent) = destination, parent.source == item.source,
            repository.protectedItem(parent).protection == nil {
            try await repository.move(item,to:parent,name:item.file.name)
            return nil
        }
        if !copying, (try await companion.capabilities().features?.contains("archiveLinksV1")) == true {
            let location = try companion.sourceLocation(CopyLocation(source: item.source, path: item.file.path))
            let links = try await companion.archiveLinks()
            if links.contains(where: { link in
                [link.library, link.archive].contains { $0.source == location.source && FilePath.contains($0.path, in: location.path) }
            }) {
                throw NSError(domain: "fileBeans.LinkedArchive", code: 409,
                              userInfo: [NSLocalizedDescriptionKey: "Unlink the archive before moving this folder across sources."])
            }
        }
        let job: CopyJob?
        switch destination {
        case .drive(let parent):
            job = try await companion.submit(BackgroundCopy(items:[CopyLocation(source:item.source,path:item.file.path)],
                destination:CopyLocation(source:"drive",path:parent.id,name:parent.name)))
        case .local(let parent):
            guard parent.permissions.create,
                item.source != parent.source || !FilePath.contains(parent.file.path,in:item.file.path) else { throw APIError.forbidden }
            if repository.protectedItem(parent).protection != nil {
                job = try await companion.submit(BackgroundCopy(items:[CopyLocation(source:item.source,path:item.file.path)],
                    destination:CopyLocation(source:parent.source,path:parent.file.path)))
            } else {
                let children = try await repository.children(item)
                let code = try await VaultKeySession.shared.authorizedCode()
                let canonical = try companion.sourceLocation(CopyLocation(source:parent.source,path:parent.file.path))
                let created = try await companion.createProtectedFolder(source:parent.source,parent:canonical.path,
                    name:item.file.name,code:code,requestID:UUID().uuidString)
                try repository.store.setVaults(await companion.vaults(),connection:repository.connection.id)
                if children.isEmpty { job = nil }
                else {
                    // The API expects scope-relative inputs; submit canonicalizes them.
                    let path = try FilePath.child(item.file.name,in:parent.file.path)
                    job = try await companion.submit(BackgroundCopy(items:children.map { CopyLocation(source:$0.source,path:$0.file.path) },
                        destination:CopyLocation(source:parent.source,path:path)))
                }
                _ = created
            }
        }
        if !copying {
            if let job { try await companion.waitForMove(job) }
            try await companion.trash(item)
            return nil
        }
        return job
    }
    func enqueue(urls: [URL], parent: StoredItem, scoped: Bool = true) async {
        do {
            guard let repository, parent.permissions.create else { throw APIError.forbidden }
            try repository.requireUnlocked(parent)
            try repository.requireArchiveWritable(parent)
            try await UploadQueue.shared.enqueue(urls, connection: repository.connection, source: parent.source, parent: parent.file.path, scoped: scoped)
        } catch { self.error = error.localizedDescription }
    }
}
