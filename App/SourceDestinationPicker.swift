import SwiftUI

enum BrowserDestination: Identifiable {
    case local(StoredItem)
    case drive(DriveFile)
    var id: String {
        switch self { case .local(let item): return "local:" + item.id; case .drive(let item): return "drive:" + item.id }
    }
    var isFolder: Bool { switch self { case .local(let item): return item.file.isDirectory; case .drive(let item): return item.folder } }
    var name: String { switch self { case .local(let item): return item.file.name; case .drive(let item): return item.name } }
}
enum DestinationMode {
    case copy
    case localMove(String?)
    case driveMove
    var copying: Bool { if case .copy = self { return true }; return false }
    var allowsLocal: Bool { true }
    var allowsDrive: Bool { if case .localMove(let source) = self { return source == nil }; return true }
}
struct SourceDestinationPicker: View {
    let connection: Connection
    let mode: DestinationMode
    let excludedLocal: [StoredItem]
    let excludedDrive: Set<String>
    var folder: BrowserDestination? = nil
    let choose: (BrowserDestination) -> Void
    @State private var folders: [BrowserDestination] = []
    @State private var resolvedDrive: DriveFile?
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
        case .local(let item): return item.permissions.create
        case .drive(let item): return item.capabilities?.canAddChildren == true && !excludedDrive.contains(item.id)
        }
    }
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            ForEach(folders) { child in
                if child.isFolder { NavigationLink {
                    SourceDestinationPicker(connection: connection, mode: mode, excludedLocal: excludedLocal,
                                            excludedDrive: excludedDrive, folder: child, choose: choose)
                } label: { Label(child.name, systemImage: "folder") }
                } else { Label(child.name, systemImage: "doc").foregroundStyle(.secondary) }
            }
            if !next.isEmpty { Button("Load More") { Task { await load(page: next) } } }
        }
        .navigationTitle(folder?.name ?? (mode.copying ? "Copy To" : "Move To"))
        .safeAreaInset(edge: .bottom) {
            if let target {
                Button(mode.copying ? "Copy Here" : "Move Here") { choose(target) }
                    .buttonStyle(.borderedProminent).padding().disabled(!canChoose || loading || error != nil)
            }
        }
        .overlay { if loading { ProgressView() } }
        .task { await load() }
    }
    private func load(page: String = "") async {
        loading = true; defer { loading = false }
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
                if mode.allowsLocal {
                    let repository = try Repository(connection: connection, store: SharedStore())
                    result = try await repository.roots().filter(allowedLocal).map(BrowserDestination.local)
                }
                if mode.allowsDrive, connection.companionURL != nil {
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    if (try? await client.capabilities().driveWriteAccess) == true {
                        result.append(.drive(DriveFile(id: "root", name: "Google Drive", mimeType: "application/vnd.google-apps.folder", sourceRoot: true)))
                    }
                }
            }
            if page.isEmpty { folders = result } else { folders += result.filter { child in !folders.contains { $0.id == child.id } } }
            error = nil
        } catch { folders = []; self.error = error.localizedDescription }
    }
    private func allowedLocal(_ item: StoredItem) -> Bool {
        guard item.protection?.unlocked != false else { return false }
        if case .localMove(let source) = mode, let source, item.source != source { return false }
        if connection.companionURL != nil && CompanionClient.protected(item.file.path) { return false }
        return !excludedLocal.contains { selected in
            selected.source == item.source && selected.file.isDirectory && FilePath.contains(item.file.path, in: selected.file.path)
        }
    }
}
