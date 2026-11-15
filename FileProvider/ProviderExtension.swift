import FileProvider
import UniformTypeIdentifiers

final class ProviderExtension: NSObject, NSFileProviderReplicatedExtension, NSFileProviderThumbnailing {
    let domain: NSFileProviderDomain
    required init(domain: NSFileProviderDomain) { self.domain = domain; super.init() }
    func invalidate() {}

    private func repository() throws -> Repository {
        let store = try SharedStore()
        guard let connection = try store.connections().first(where: { $0.id.uuidString == domain.identifier.rawValue }) else {
            throw APIError.authentication
        }
        return try Repository(connection: connection, store: store)
    }
    private func root() -> ProviderItem {
        ProviderItem(StoredItem(id: NSFileProviderItemIdentifier.rootContainer.rawValue, parent: "root", source: "",
            file: RemoteFile(name: domain.displayName, path: "/", type: "directory"), permissions: Permissions(view: true)))
    }
    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest,
                       completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void) -> Progress {
        operation {
            do {
                if identifier == .rootContainer { completionHandler(self.root(), nil); return }
                let repo = try self.repository()
                _ = try await repo.roots()
                let cached = try repo.item(identifier.rawValue)
                if cached.isSource { completionHandler(ProviderItem(cached), nil); return }
                let resource = try await repo.client.resource(source: cached.source, path: cached.file.path)
                var item = cached; item.file = resource.file
                completionHandler(ProviderItem(item), nil)
            } catch { completionHandler(nil, providerError(error)) }
        }
    }
    func fetchContents(for itemIdentifier: NSFileProviderItemIdentifier,
                                version requestedVersion: NSFileProviderItemVersion?, request: NSFileProviderRequest,
                                completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void) -> Progress {
        operation {
            do {
                let repo = try self.repository()
                _ = try await repo.roots()
                var item = try repo.item(itemIdentifier.rawValue)
                guard item.permissions.download else { throw APIError.forbidden }
                item.file = try await repo.client.resource(source: item.source, path: item.file.path).file
                if let requestedVersion, requestedVersion.contentVersion != item.file.version { throw APIError.conflict }
                let url = try await repo.client.download(source: item.source, path: item.file.path)
                do {
                    let latest = try await repo.client.resource(source: item.source, path: item.file.path)
                    guard latest.file.version == item.file.version else { throw APIError.conflict }
                } catch {
                    try? FileManager.default.removeItem(at: url)
                    throw error
                }
                completionHandler(url, ProviderItem(item), nil)
            } catch { completionHandler(nil, nil, providerError(error)) }
        }
    }
    func createItem(basedOn itemTemplate: NSFileProviderItem, fields: NSFileProviderItemFields,
                             contents url: URL?, options: NSFileProviderCreateItemOptions, request: NSFileProviderRequest,
                             completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        operation {
            do {
                let repo = try self.repository()
                _ = try await repo.roots()
                let parent = try repo.item(itemTemplate.parentItemIdentifier.rawValue)
                guard parent.permissions.create else { throw APIError.forbidden }
                let path = try FilePath.child(itemTemplate.filename, in: parent.file.path)
                if itemTemplate.contentType == .folder {
                    try await repo.client.createFolder(source: parent.source, path: path)
                } else {
                    guard let url else { throw CocoaError(.fileReadNoSuchFile) }
                    try await repo.client.upload(source: parent.source, path: path, file: url)
                }
                let children = try await repo.children(parent)
                guard let created = children.first(where: { $0.file.path == path }) else { throw APIError.missing }
                repo.notify(parent.id)
                completionHandler(ProviderItem(created), [], false, nil)
            } catch { completionHandler(nil, fields, false, providerError(error)) }
        }
    }
    func modifyItem(_ item: NSFileProviderItem, baseVersion version: NSFileProviderItemVersion,
                             changedFields: NSFileProviderItemFields, contents newContents: URL?,
                             options: NSFileProviderModifyItemOptions, request: NSFileProviderRequest,
                             completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?) -> Void) -> Progress {
        operation {
            do {
                let repo = try self.repository()
                _ = try await repo.roots()
                var stored = try repo.item(item.itemIdentifier.rawValue)
                guard !stored.isSource, stored.permissions.modify else { throw APIError.forbidden }
                guard version.metadataVersion == ProviderItem(stored).itemVersion.metadataVersion else { throw APIError.conflict }
                let remote = try await repo.client.resource(source: stored.source, path: stored.file.path)
                guard version.contentVersion == remote.file.version else { throw APIError.conflict }
                let unsupported = changedFields.subtracting([.contents, .filename, .parentItemIdentifier, .contentModificationDate])
                guard unsupported.isEmpty else { throw CocoaError(.featureUnsupported) }
                var remaining = changedFields
                remaining.remove(.contentModificationDate)
                var metadataChanged = false
                if changedFields.contains(.filename) || changedFields.contains(.parentItemIdentifier) {
                    let parent = try repo.item(item.parentItemIdentifier.rawValue)
                    try await repo.move(stored, to: parent, name: item.filename)
                    stored = try repo.item(stored.id)
                    remaining.subtract([.filename, .parentItemIdentifier])
                    metadataChanged = true
                }
                if changedFields.contains(.contents) {
                    do {
                        guard let newContents else { throw CocoaError(.fileReadNoSuchFile) }
                        try await repo.client.upload(source: stored.source, path: stored.file.path, file: newContents, replacing: true)
                        remaining.remove(.contents)
                    } catch {
                        if metadataChanged {
                            // Acknowledge the completed move while keeping local contents pending.
                            let current = try await repo.client.resource(source: stored.source, path: stored.file.path)
                            stored.file = current.file
                            completionHandler(ProviderItem(stored), remaining, false, nil)
                            return
                        }
                        throw error
                    }
                }
                _ = try await repo.children(repo.item(stored.parent))
                repo.notify(stored.parent)
                completionHandler(ProviderItem(try repo.item(stored.id)), [], false, nil)
            } catch { completionHandler(nil, changedFields, false, providerError(error)) }
        }
    }
    func deleteItem(identifier: NSFileProviderItemIdentifier, baseVersion version: NSFileProviderItemVersion,
                             options: NSFileProviderDeleteItemOptions, request: NSFileProviderRequest,
                             completionHandler: @escaping (Error?) -> Void) -> Progress {
        operation {
            do {
                let repo = try self.repository()
                _ = try await repo.roots()
                let item = try repo.item(identifier.rawValue)
                guard version.metadataVersion == ProviderItem(item).itemVersion.metadataVersion else { throw APIError.conflict }
                let remote = try await repo.client.resource(source: item.source, path: item.file.path)
                guard version.contentVersion == remote.file.version else { throw APIError.conflict }
                if item.file.isDirectory && !options.contains(.recursive) {
                    let children = try await repo.children(item)
                    guard children.isEmpty else { throw NSFileProviderError(.directoryNotEmpty) }
                }
                try await repo.delete(item)
                completionHandler(nil)
            } catch { completionHandler(providerError(error)) }
        }
    }
    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier,
                             request: NSFileProviderRequest) throws -> NSFileProviderEnumerator {
        ProviderEnumerator(repository: try repository(), identifier: containerItemIdentifier)
    }
    func fetchThumbnails(for itemIdentifiers: [NSFileProviderItemIdentifier], requestedSize size: CGSize,
                                  perThumbnailCompletionHandler: @escaping (NSFileProviderItemIdentifier, Data?, Error?) -> Void,
                                  completionHandler: @escaping (Error?) -> Void) -> Progress {
        operation {
            do {
                let repo = try self.repository()
                for id in itemIdentifiers {
                    do {
                        try Task.checkCancellation()
                        let item = try repo.item(id.rawValue)
                        let data = try await repo.client.thumbnail(source: item.source, file: item.file)
                        perThumbnailCompletionHandler(id, data, nil)
                    } catch { perThumbnailCompletionHandler(id, nil, providerError(error)) }
                }
                completionHandler(nil)
            } catch { completionHandler(providerError(error)) }
        }
    }
    private func operation(_ work: @escaping () async -> Void) -> Progress {
        let progress = Progress(totalUnitCount: 1)
        let task = Task { await work(); progress.completedUnitCount = 1 }
        progress.cancellationHandler = { task.cancel() }
        return progress
    }
}
