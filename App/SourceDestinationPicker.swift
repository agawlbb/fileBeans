import SwiftUI

enum BrowserDestination: Identifiable {
    case local(StoredItem)
    case drive(DriveFile)
    var id: String {
        switch self { case .local(let item): return "local:" + item.id; case .drive(let item): return "drive:" + item.id }
    }
    var isFolder: Bool { switch self { case .local(let item): return item.file.isDirectory; case .drive(let item): return item.folder } }
    var name: String { switch self { case .local(let item): return item.file.name; case .drive(let item): return item.name } }
    var sortValue: BrowserSortValue {
        BrowserSortValue(id: id, name: name, folder: isFolder, size: 0, modified: .distantPast)
    }
}
struct SourceSelection {
    let connection: Connection
    let destination: BrowserDestination
}
enum DestinationMode {
    case copy
    case archive
    case upload
    case localMove(String?)
    case driveMove
    var isArchive: Bool { if case .archive = self { return true }; return false }
    var title: String { if isArchive { return "Archive Folder" }; if case .upload = self { return "Upload To" }; return copying ? "Copy To" : "Move To" }
    var action: String { if isArchive { return "Link Archive" }; if case .upload = self { return "Upload" }; return copying ? "Copy Here" : "Move Here" }
    var copying: Bool { if case .copy = self { return true }; return false }
    var allowsLocal: Bool { true }
    var allowsDrive: Bool { if isArchive { return false }; if case .localMove(let source) = self { return source == nil }; return true }
}
struct SourceDestinationPicker: View {
    let connection: Connection
    let mode: DestinationMode
    let excludedLocal: [StoredItem]
    let excludedDrive: Set<String>
    var folder: BrowserDestination? = nil
    var showSources = true
    var originConnection: UUID? = nil
    var protectedFolderSheet: ((Connection, StoredItem) -> AnyView)? = nil
    let choose: (SourceSelection) -> Void
    @State private var folders: [BrowserDestination] = []
    @State private var resolvedDrive: DriveFile?
    @State private var newProtectedFolder = false
    @State private var loading = true
    @State private var error: String?
    @State private var next = ""
    private var target: BrowserDestination? {
        if let resolvedDrive { return .drive(resolvedDrive) }
        return folder
    }
    private var canChoose: Bool {
        guard let target else { return false }
        switch target {
        case .local(let item): return item.permissions.create && (!mode.isArchive || (!item.isSource && item.file.archiveLink == nil))
        case .drive(let item): return item.capabilities?.canAddChildren == true && !excludedDrive.contains(item.id)
        }
    }
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            if showSources && folder == nil {
                ForEach(availableSources) { source in
                    NavigationLink(source.name) {
                        SourceDestinationPicker(connection: source, mode: mode, excludedLocal: excludedLocal,
                            excludedDrive: excludedDrive, showSources: false, originConnection: originConnection ?? connection.id,
                            protectedFolderSheet: protectedFolderSheet, choose: choose)
                    }
                }
            }
            ForEach(folders) { child in
                if child.isFolder { NavigationLink {
                    SourceDestinationPicker(connection: connection, mode: mode, excludedLocal: excludedLocal,
                                            excludedDrive: excludedDrive, folder: child, showSources: false, originConnection: originConnection ?? connection.id,
                                            protectedFolderSheet: protectedFolderSheet, choose: choose)
                } label: { Label(child.name, systemImage: "folder") }
                } else { Label(child.name, systemImage: "doc").foregroundStyle(.secondary) }
            }
            if !next.isEmpty { Button("Load More") { Task { await load(page: next) } } }
        }
        .navigationTitle(folder?.name ?? mode.title)
        .safeAreaInset(edge: .bottom) {
            if let target {
                Button(mode.action) { choose(SourceSelection(connection: connection, destination: target)) }
                    .buttonStyle(.borderedProminent).padding().disabled(!canChoose || loading || error != nil)
            }
        }
        .toolbar {
            if mode.isArchive, protectedFolderSheet != nil, case .local(let parent) = folder, parent.permissions.create, parent.protection == nil {
                Button("Add Protected Folder", systemImage: "folder.badge.plus") { newProtectedFolder = true }
            }
        }
        .sheet(isPresented: $newProtectedFolder, onDismiss: { Task { await load() } }) {
            if case .local(let parent) = folder, let protectedFolderSheet { protectedFolderSheet(connection, parent) }
        }
        .overlay { if loading { ProgressView() } }
        .task { await load() }
    }
    private var availableSources: [Connection] {
        ((try? SharedStore().connections()) ?? []).filter { source in
            if source.isGoogleDrive { return mode.allowsDrive }
            if case .localMove(let restricted) = mode, restricted != nil { return source.id == connection.id }
            return mode.allowsLocal
        }
    }
    private func load(page: String = "") async {
        loading = true; defer { loading = false }
        if showSources && folder == nil { folders = []; error = nil; return }
        do {
            var result: [BrowserDestination] = []
            switch folder {
            case .drive(let item):
                let client = DriveClient(connection: connection); defer { client.close() }
                resolvedDrive = try await client.file(item.id)
                let list = try await client.list(folder: item.id, page: page)
                result = list.files.filter { !excludedDrive.contains($0.id) }.map(BrowserDestination.drive)
                next = list.nextPageToken ?? ""
                if item.id == "root", page.isEmpty {
                    var drivePage = ""
                    repeat {
                        let list = try await client.drives(page: drivePage)
                        result += list.drives.map { .drive(DriveFile(id: $0.id, name: $0.name, mimeType: "application/vnd.google-apps.folder", sourceRoot: true)) }
                        drivePage = list.nextPageToken ?? ""
                    } while !drivePage.isEmpty
                }
            case .local(let item):
                let repository = try Repository(connection: connection, store: SharedStore())
                result = try await repository.children(item).filter(allowedLocal).map(BrowserDestination.local)
            case nil:
                if mode.allowsLocal, !connection.isGoogleDrive {
                    let repository = try Repository(connection: connection, store: SharedStore())
                    result = try await repository.roots().filter(allowedLocal).map(BrowserDestination.local)
                }
                if mode.allowsDrive, connection.isGoogleDrive {
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    if (try? await client.capabilities().driveWriteAccess) == true {
                        result.append(.drive(DriveFile(id: "root", name: "Google Drive", mimeType: "application/vnd.google-apps.folder", sourceRoot: true)))
                    }
                }
            }
            if page.isEmpty { folders = result } else { folders += result.filter { child in !folders.contains { $0.id == child.id } } }
            folders.sort { BrowserOrdering.precedes($0.sortValue, $1.sortValue, order: "name") }
            error = nil
        } catch { folders = []; self.error = error.localizedDescription }
    }
    private func allowedLocal(_ item: StoredItem) -> Bool {
        guard item.protection?.unlocked != false else { return false }
        if case .localMove(let source) = mode, let source, item.source != source { return false }
        if connection.companionURL != nil && CompanionClient.protected(item.file.path) { return false }
        if let originConnection, originConnection != connection.id { return true }
        return !excludedLocal.contains { selected in
            selected.source == item.source && selected.file.isDirectory && FilePath.contains(item.file.path, in: selected.file.path)
        }
    }
}
