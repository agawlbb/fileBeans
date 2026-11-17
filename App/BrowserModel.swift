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
            if let parent {
                // Refresh access before each listing, including after token replacement.
                let roots = try await repository.roots()
                guard let root = roots.first(where: { $0.source == parent.source }),
                      FilePath.contains(parent.file.path, in: root.file.path) else { throw APIError.forbidden }
                var refreshed = parent; refreshed.permissions = root.permissions
                items = try await repository.children(refreshed)
                parentProtection = repository.protectedItem(refreshed).protection
            } else { items = try await repository.roots() }
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
