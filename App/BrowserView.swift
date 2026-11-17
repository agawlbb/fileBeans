import SwiftUI
import QuickLook
import AVKit
import PhotosUI
import UniformTypeIdentifiers

struct BrowserView: View {
    let connection: Connection
    let parent: StoredItem?
    @StateObject private var model = BrowserModel()
    @State private var search = ""
    @AppStorage("gridView") private var grid = false
    @AppStorage("sortOrder") private var sortOrder = "name"
    @State private var selecting = false
    @State private var selected = Set<String>()
    @State private var renameItem: StoredItem?
    @State private var proposedName = ""
    @State private var newFolder = false
    @State private var newProtectedFolder = false
    @State private var protectedSelection: StoredItem?
    @State private var deleting = false
    @State private var moving = false
    @State private var copying = false
    @State private var copyInProgress = false
    @State private var importing = false
    @State private var preview: URL?
    @State private var movie: Movie?
    @State private var photos: [PhotosPickerItem] = []
    @Environment(\.scenePhase) private var scenePhase
    private var visible: [StoredItem] {
        model.items.filter {
            (connection.companionURL == nil || $0.file.path != "/Trash") &&
            (search.isEmpty || $0.file.name.localizedStandardContains(search))
        }.sorted {
            BrowserOrdering.precedes(sortValue($0), sortValue($1), order: sortOrder)
        }
    }
    private func sortValue(_ item: StoredItem) -> BrowserSortValue {
        BrowserSortValue(id: item.id, name: item.file.name, folder: item.file.isDirectory,
                         size: item.file.size ?? 0, modified: item.file.date ?? .distantPast)
    }
    private var chosen: [StoredItem] { model.items.filter { selected.contains($0.id) } }
    var body: some View {
        browserWithActions
        .fileImporter(isPresented: $importing, allowedContentTypes: [.data], allowsMultipleSelection: true) { result in
            do { if let parent { model.enqueue(urls: try result.get(), parent: parent) } }
            catch { model.error = error.localizedDescription }
        }
        .onChange(of: photos) { _, picks in
            Task {
                for pick in picks {
                    do {
                        if let file = try await pick.loadTransferable(type: ImportedMedia.self), let parent {
                            model.enqueue(urls: [file.url], parent: parent, scoped: false)
                            try? FileManager.default.removeItem(at: file.url.deletingLastPathComponent())
                        }
                    } catch { model.error = error.localizedDescription }
                }
                photos = []
            }
        }
        .quickLookPreview($preview)
        .onChange(of: preview) { previous, current in
            if current == nil, let previous { try? FileManager.default.removeItem(at: previous.deletingLastPathComponent()) }
        }
        .sheet(item: $movie) { MovieView(movie: $0) }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await reload() } }
        }
        .sheet(isPresented: $newProtectedFolder) {
            if let parent {
                NewProtectedFolder(connection: connection, parent: parent) { Task { await reload() } }
            }
        }
        .confirmationDialog(protectedSelection?.file.name ?? "Protected Folder", isPresented: Binding(
            get: { protectedSelection != nil }, set: { if !$0 { protectedSelection = nil } }), titleVisibility: .visible) {
            if let item = protectedSelection {
                Button("Unlock") { changeLock(item, action: "unlock"); protectedSelection = nil }
            }
        }
    }
    private enum SourceEntry: Identifiable {
        case local(StoredItem)
        case drive
        var id: String { switch self { case .local(let item): return item.id; case .drive: return "google-drive" } }
        var name: String { switch self { case .local(let item): return item.file.name; case .drive: return "Google Drive" } }
    }
    private var sources: [SourceEntry] {
        var result = visible.map(SourceEntry.local)
        if search.isEmpty || "Google Drive".localizedStandardContains(search) { result.append(.drive) }
        return result.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
    }
    @ViewBuilder private var fileContent: some View {
        if parent == nil {
            BrowserFileCollection(items: sources, grid: grid, header: companionLinks, row: { source, large in sourceEntry(source, large: large) })
        } else {
            BrowserFileCollection(items: visible, grid: grid, header: companionLinks, row: { item, large in entry(item, large: large) })
        }
    }
    @ViewBuilder private func sourceEntry(_ source: SourceEntry, large: Bool) -> some View {
        switch source {
        case .local(let item): entry(item, large: large)
        case .drive:
            NavigationLink(value: SourceDestination(connection: connection, kind: .drive)) {
                BrowserItemCell(name: source.name, size: nil, folder: true, large: large, selecting: false, selected: false,
                                thumbnail: Image(systemName: "folder.fill").resizable().scaledToFit().padding(8).foregroundStyle(Color.accentColor))
            }.disabled(selecting)
        }
    }
    @ViewBuilder private var companionLinks: some View {
        if parent == nil {
            if connection.companionURL != nil {
                NavigationLink { TransferList(connection: connection) } label: { Label("Transfers", systemImage: "arrow.left.arrow.right") }
                Text("Deletion protection: 10 days").font(.caption).foregroundStyle(.secondary)
            }
        }
    }
    private var browserContent: some View {
        fileContent
        // The hierarchy replaces the large folder heading below the controls.
        .navigationTitle("")
        .navigationBarTitleDisplayMode(.inline)
        .safeAreaInset(edge: .top, spacing: 0) {
            FolderBreadcrumbHeading(fallbackTitle: parent?.file.name ?? connection.name)
        }
        .searchable(text: $search, prompt: "Search this folder")
        .overlay { emptyState }
                .safeAreaInset(edge: .bottom) {
            VStack(spacing: 0) {
                transferStatus
                if let parent, parent.isSource, connection.companionURL != nil {
                    SourceTrashBar(destination: TrashList(connection: connection, source: parent.source))
                }
            }
        }
        .toolbar { browserToolbar }
        .refreshable { await reload() }
        .task {
            await reload()
            guard connection.companionURL != nil else { return }
            // Reconcile completed background copies while this folder is visible.
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
                await reload()
            }
        }
    }
    private var browserWithAlerts: some View {
        browserContent
        .alert("fileBeans", isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
            Button("OK") { model.error = nil }
            Button("Refresh") { Task { await reload() } }
        } message: { Text(model.error ?? "") }
        .alert("Rename", isPresented: Binding(get: { renameItem != nil }, set: { if !$0 { renameItem = nil } })) {
            TextField("Name", text: $proposedName)
            Button("Cancel", role: .cancel) { renameItem = nil }
            Button("Save") {
                guard let item = renameItem, let parent else { return }
                renameItem = nil
                Task { await model.perform { try await model.repository?.move(item, to: parent, name: proposedName); await reload() } }
            }
        }
        .alert("New Folder", isPresented: $newFolder) {
            TextField("Name", text: $proposedName)
            Button("Cancel", role: .cancel) {}
            Button("Create") {
                guard let parent else { return }
                Task { await model.perform { try await model.repository?.createFolder(name: proposedName, parent: parent); await reload() } }
            }
        }
    }
    private var browserWithActions: some View {
        browserWithAlerts
        .confirmationDialog(connection.companionURL == nil ? "Delete \(chosen.count) item(s)?" : "Move \(chosen.count) item(s) to Trash for 10 days?", isPresented: $deleting, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                let items = chosen
                Task {
                    let succeeded = await model.perform { for item in items { try await model.repository?.delete(item) } }
                    if succeeded { selected.removeAll(); selecting = false; await reload() }
                }
            }
        }
        .sheet(isPresented: $moving) {
            if let repository = model.repository, let parent {
                NavigationStack {
                    SourceDestinationPicker(connection: connection,
                                            mode: copying ? .copy : .localMove(parent.source),
                                            excludedLocal: chosen, excludedDrive: []) { destination in
                        let items = chosen
                        let isCopy = copying
                        moving = false
                        Task {
                            copyInProgress = isCopy
                            defer { copyInProgress = false }
                            let succeeded = await model.perform {
                                switch destination {
                                case .local(let folder):
                                    if isCopy { try await repository.copy(items, to: folder) }
                                    else { for item in items { try await repository.move(item, to: folder, name: item.file.name) } }
                                case .drive(let folder):
                                    guard isCopy, let companion = repository.companion else { throw APIError.forbidden }
                                    _ = try await companion.submit(BackgroundCopy(items: items.map { CopyLocation(source: $0.source, path: $0.file.path) }, destination: CopyLocation(source: "drive", path: folder.id, name: folder.name)))
                                }
                            }
                            if succeeded { selected.removeAll(); selecting = false; await reload() }
                        }
                    }.toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { moving = false } } }
                }
            }
        }
    }
    @ViewBuilder private var emptyState: some View {
            if model.busy && visible.isEmpty { ProgressView() }
            else if let protection = model.parentProtection, !protection.unlocked, let item = parent {
                ContentUnavailableView {
                    Label(item.file.name, systemImage: "lock.fill")
                } actions: {
                    Button("Unlock") {
                        var lockedItem = item; lockedItem.protection = protection
                        changeLock(lockedItem, action: "unlock")
                    }
                }
            }
            else if model.items.filter({ connection.companionURL == nil || $0.file.path != "/Trash" }).isEmpty && model.error == nil && parent != nil {
                ContentUnavailableView("No files", systemImage: "folder")
            } else if parent == nil && sources.isEmpty || parent != nil && !model.items.isEmpty && visible.isEmpty {
                ContentUnavailableView.search(text: search)
            }
    }
    @ViewBuilder private var transferStatus: some View {
            if copyInProgress {
                HStack { ProgressView(); Text(connection.companionURL == nil ? "Copying on server…" : "Queuing server copy…") }
                    .padding().background(.regularMaterial)
            } else if let progress = model.uploadProgress {
                HStack {
                    VStack(alignment: .leading) {
                        Text(model.uploadName).lineLimit(1)
                        ProgressView(value: progress)
                    }
                    Button("Cancel", systemImage: "xmark") { model.cancel() }.labelStyle(.iconOnly)
                }.padding().background(.regularMaterial)
            } else if model.canRetry {
                Button("Retry Upload", systemImage: "arrow.clockwise") { model.retry() }.padding()
            }
    }
    @ToolbarContentBuilder private var browserToolbar: some ToolbarContent {
            if connection.companionURL != nil && parent != nil {
                ToolbarItem(placement: .topBarLeading) {
                    NavigationLink { TransferList(connection: connection) } label: { Label("Transfers", systemImage: "arrow.left.arrow.right") }
                }
            }
            ToolbarItemGroup(placement: .topBarTrailing) {
                if selecting {
                    BrowserSelectionControls(
                        canCopy: !model.busy && !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.download || protected($0) },
                        canMove: !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.modify },
                        canDelete: !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.delete },
                        copy: { copying = true; moving = true }, move: { copying = false; moving = true },
                        delete: { deleting = true }, done: { selecting = false; selected.removeAll() })
                } else {
                    BrowserViewOptions(grid: $grid, sortOrder: $sortOrder, select: { selecting = true }, refresh: { Task { await reload() } })
                    if let parent, parent.permissions.create, model.parentProtection?.unlocked != false {
                        Menu {
                            Button("New Folder", systemImage: "folder.badge.plus") { proposedName = ""; newFolder = true }
                            if connection.companionURL != nil {
                                Button("Add Protected Folder", systemImage: "lock.fill") { newProtectedFolder = true }
                                    .disabled(!model.canCreateProtectedFolder || model.parentProtection != nil)
                            }
                            Button("Upload Files", systemImage: "arrow.up.doc") { importing = true }
                            PhotosPicker(selection: $photos, matching: .any(of: [.images, .videos])) {
                                Label("Upload Photos or Videos", systemImage: "photo")
                            }
                        } label: { Label("Add", systemImage: "plus") }
                    }
                }
            }
    }
    @ViewBuilder private func entry(_ item: StoredItem, large: Bool) -> some View {
        Group {
            if item.file.isDirectory && !selecting && item.protection?.unlocked == false {
                Button { protectedSelection = item } label: { cell(item, large: large) }.buttonStyle(.plain)
            } else if item.file.isDirectory && !selecting {
                NavigationLink(value: SourceDestination(connection: connection, kind: .local(item))) { cell(item, large: large) }
            } else {
                Button {
                    if selecting {
                        if selected.contains(item.id) { selected.remove(item.id) } else { selected.insert(item.id) }
                    } else { open(item) }
                } label: { cell(item, large: large) }.buttonStyle(.plain)
            }
        }
        .contextMenu {
            if let protection = item.protection, protection.isRoot {
                Button(protection.unlocked ? "Lock" : "Unlock", systemImage: protection.symbol) {
                    changeLock(item, action: protection.unlocked ? "lock" : "unlock")
                }
            }
            if !item.isSource && item.permissions.download && !protected(item) {
                Button("Copy", systemImage: "doc.on.doc") { selected = [item.id]; copying = true; moving = true }
                    .disabled(model.busy)
            }
            if !item.isSource && item.permissions.modify && !protected(item) {
                Button("Rename", systemImage: "pencil") { proposedName = item.file.name; renameItem = item }
                Button("Move", systemImage: "folder") { selected = [item.id]; copying = false; moving = true }
            }
            if !item.file.isDirectory && item.permissions.download {
                Button("Open", systemImage: "arrow.up.right.square") { open(item) }
            }
            if !item.isSource && item.permissions.delete && !protected(item) {
                Button("Delete", systemImage: "trash", role: .destructive) { selected = [item.id]; deleting = true }
            }
        }
    }
    private func cell(_ item: StoredItem, large: Bool) -> some View {
        BrowserItemCell(name: item.file.name, size: item.file.size, folder: item.file.isDirectory,
                        large: large, selecting: selecting, selected: selected.contains(item.id),
                        thumbnail: ThumbnailView(item: item, client: model.repository?.client), protection: item.protection)
    }
    private func changeLock(_ item: StoredItem, action: String) {
        guard let protection = item.protection else { return }
        Task {
            await model.perform {
                let client = CompanionClient(connection: connection); defer { client.close() }
                let code = action == "unlock" ? try await VaultKeySession.shared.authorizedCode() : nil
                try await client.vaultAction(id: protection.id, action: action, code: code)
                await reload()
            }
        }
    }
    private func protected(_ item: StoredItem) -> Bool {
        item.protection?.isRoot == true || connection.companionURL != nil && CompanionClient.protected(item.file.path)
    }
    private func reload() async { await model.load(connection: connection, parent: parent) }
    private func open(_ item: StoredItem) {
        guard let client = model.repository?.client else { return }
        Task {
            await model.perform {
                if item.file.isMedia {
                    let request = try await client.streamRequest(source: item.source, path: item.file.path)
                    movie = Movie(name: item.file.name,
                                  stream: MediaStream(client: client, request: request, name: item.file.name))
                } else {
                    _ = try FilePath.child(item.file.name, in: "/")
                    let downloaded = try await client.download(source: item.source, path: item.file.path)
                    let directory = try SharedStore().cacheDirectory(connection.id).appendingPathComponent("previews/" + UUID().uuidString)
                    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    let destination = directory.appendingPathComponent(item.file.name)
                    try FileManager.default.moveItem(at: downloaded, to: destination)
                    preview = destination
                }
            }
        }
    }
}

struct ImportedMedia: Transferable {
    var url: URL
    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(importedContentType: .data) { received in
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let url = dir.appendingPathComponent(received.file.lastPathComponent)
            try FileManager.default.copyItem(at: received.file, to: url)
            return ImportedMedia(url: url)
        }
    }
}

struct Movie: Identifiable { let id = UUID(); let name: String; let stream: MediaStream }
struct MovieView: View {
    let movie: Movie
    @Environment(\.dismiss) private var dismiss
    @State private var player = AVPlayer()
    @State private var error = false
    @State private var failureDetails = ""
    @State private var started = false
    var body: some View {
        NavigationStack {
            NativeVideoPlayer(player: player, stream: movie.stream)
                .navigationTitle(movie.name).navigationBarTitleDisplayMode(.inline)
                .toolbar { Button("Done") { dismiss() } }
                .task {
                    guard !started else { return }
                    started = true
                    do { try await movie.stream.play(on: player) }
                    catch {
                        guard !Task.isCancelled else { return }
                        failureDetails = PlaybackDiagnostics.summary(error: error as NSError)
                        self.error = true
                    }
                }
                .onReceive(NotificationCenter.default.publisher(for: .AVPlayerItemFailedToPlayToEndTime)) { notification in
                    guard let item = notification.object as? AVPlayerItem, item === player.currentItem else { return }
                    showFailure(item, error: notification.userInfo?[AVPlayerItemFailedToPlayToEndTimeErrorKey] as? NSError)
                }
                .task {
                    // Initial loading failures arrive through status, without an end-of-playback notification.
                    while !Task.isCancelled {
                        if let item = player.currentItem, item.status == .failed { showFailure(item); break }
                        try? await Task.sleep(for: .seconds(1))
                    }
                }
                .alert("Unable to Play", isPresented: $error) { Button("OK") { dismiss() } } message: {
                    Text("Playback failed.\n\n\(failureDetails)")
                }
        }
    }
    private func showFailure(_ item: AVPlayerItem, error: NSError? = nil) {
        failureDetails = PlaybackDiagnostics.summary(
            error: error ?? item.error.map { $0 as NSError },
            mediaStatusCodes: item.errorLog()?.events.map { $0.errorStatusCode } ?? [])
        self.error = true
    }
}

/// AVKit owns full screen transitions, keeping one player and resource loader alive.
struct NativeVideoPlayer: UIViewControllerRepresentable {
    let player: AVPlayer
    let stream: MediaStream
    @MainActor final class Coordinator {
        let player: AVPlayer
        let stream: MediaStream
        init(player: AVPlayer, stream: MediaStream) { self.player = player; self.stream = stream }
        func close() { player.pause(); player.replaceCurrentItem(with: nil); stream.cancel() }
    }
    func makeCoordinator() -> Coordinator { Coordinator(player: player, stream: stream) }
    static func dismantleUIViewController(_ controller: AVPlayerViewController, coordinator: Coordinator) {
        coordinator.close()
        controller.player = nil
    }
    func makeUIViewController(context: Context) -> AVPlayerViewController {
        let controller = AVPlayerViewController()
        controller.player = player
        controller.showsPlaybackControls = true
        controller.entersFullScreenWhenPlaybackBegins = false
        controller.exitsFullScreenWhenPlaybackEnds = false
        return controller
    }
    func updateUIViewController(_ controller: AVPlayerViewController, context: Context) {
        if controller.player !== player { controller.player = player }
    }
}
