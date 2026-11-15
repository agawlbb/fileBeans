import FileProvider

final class ProviderEnumerator: NSObject, NSFileProviderEnumerator {
    let repository: Repository
    let identifier: NSFileProviderItemIdentifier
    init(repository: Repository, identifier: NSFileProviderItemIdentifier) {
        self.repository = repository; self.identifier = identifier
    }
    func invalidate() {}
    private func snapshot() async throws -> [StoredItem] {
        if identifier == .workingSet {
            try await repository.refreshKnownFolders()
            return Array(try repository.store.items(repository.connection.id).values)
        }
        return try await repository.children(identifier == .rootContainer ? nil : repository.item(identifier.rawValue))
    }
    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        Task {
            do {
                let items = try await snapshot()
                observer.didEnumerate(items.map(ProviderItem.init))
                observer.finishEnumerating(upTo: nil)
            } catch { observer.finishEnumeratingWithError(providerError(error)) }
        }
    }
    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        Task {
            do {
                // Full reconciliation avoids pretending the server supplies durable change cursors.
                _ = try await snapshot()
                guard let string = String(data: anchor.rawValue, encoding: .utf8), let since = Int64(string) else {
                    throw NSFileProviderError(.syncAnchorExpired)
                }
                let parent = identifier == .rootContainer ? "root" : identifier.rawValue
                let (items, deleted, revision) = try repository.store.transaction { state in
                    let key = repository.connection.id.uuidString
                    let current = state.items[key] ?? [:]
                    let items = current.values.filter { identifier == .workingSet || $0.parent == parent }
                    let deleted = Set((state.deleted[key] ?? [:]).values.filter {
                        $0.revision > since && (identifier == .workingSet ? current[$0.id] == nil :
                            $0.parent == parent && current[$0.id]?.parent != parent)
                    }.map { NSFileProviderItemIdentifier($0.id) })
                    return (Array(items), Array(deleted), state.revisions[key] ?? 0)
                }
                observer.didDeleteItems(withIdentifiers: deleted)
                observer.didUpdate(items.map(ProviderItem.init))
                observer.finishEnumeratingChanges(upTo: NSFileProviderSyncAnchor(Data(String(revision).utf8)), moreComing: false)
            } catch { observer.finishEnumeratingWithError(providerError(error)) }
        }
    }
    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        do {
            completionHandler(NSFileProviderSyncAnchor(Data(String(try repository.store.revision(repository.connection.id)).utf8)))
        } catch { completionHandler(nil) }
    }
}
