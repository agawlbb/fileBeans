import SwiftUI
import QuickLook
import AVKit
import PhotosUI
import UniformTypeIdentifiers

struct BrowserView: View {
    let connection: Connection
    let parent: StoredItem?
    @StateObject private var model = BrowserModel()
    @ObservedObject private var activity = FileActivity.shared
    @ObservedObject private var uploads = UploadActivity.shared
    @State private var search = ""
    @AppStorage("gridView") private var grid = false
    @AppStorage("sortOrder") private var sortOrder = "name"
    @StateObject private var downloads = BrowserDownloads()
    @State private var hosting: StoredItem?
    @State private var selecting = false
    @State private var selected = Set<String>()
    @State private var renameItem: StoredItem?
    @State private var proposedName = ""
    @State private var renameSelection: TextSelection?
    @State private var archiveFolder: StoredItem?
    @State private var newFolder = false
    @State private var newTextFile = false
    @State private var editingText: StoredItem?
    @State private var newProtectedFolder = false
    @State private var protectedSelection: StoredItem?
    @State private var deleting = false
    @State private var moving = false
    @State private var copying = false
    @State private var copyInProgress = false
    @State private var importing = false
    @State private var viewer: FolderViewerSession?
    @State private var pickingPhotos = false
    @State private var photos: [PhotosPickerItem] = []
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.folderBreadcrumbs) private var trail
    @State private var visible: [StoredItem] = []
    @State private var listingTask: Task<Void, Never>?
    private func updateListing() {
        listingTask?.cancel()
        listingTask = Task { @MainActor in
            do { try await Task.sleep(for: .milliseconds(50)) } catch { return }
            let values = displayedItems, query = search, order = sortOrder
            let hideTrash = connection.companionURL != nil
            let result = await Task.detached(priority: .userInitiated) {
                values.filter {
                    (!hideTrash || $0.file.path != "/Trash") && (query.isEmpty || $0.file.name.localizedStandardContains(query))
                }.map { item in
                    (item, BrowserSortValue(id: item.id, name: item.file.name, folder: item.file.isDirectory,
                        size: item.file.size ?? 0, modified: BrowserOrdering.isModified(order) ? item.file.date ?? .distantPast : .distantPast))
                }.sorted { BrowserOrdering.precedes($0.1, $1.1, order: order) }.map { $0.0 }
            }.value
            guard !Task.isCancelled else { return }
            visible = result
        }
    }

    private var incoming: [ActivityFile] {
        guard let parent else { return [] }
        let scope = model.sourceScope
        let canonical = (try? FilePath.inSource(parent.file.path, scope: scope)) ?? parent.file.path
        return activity.pending(connection: connection.id, source: parent.source, parent: parent.file.path, canonicalParent: canonical)
    }
    private var displayedItems: [StoredItem] {
        var result = model.items.filter { $0.protection?.unlocked != false }
        guard let parent, model.parentProtection?.unlocked != false else { return result }
        for file in incoming where !result.contains(where: { $0.file.path == file.path }) {
            result.append(StoredItem(id: "pending:" + file.path, parent: parent.id, source: parent.source,
                file: RemoteFile(name: file.name, path: file.path, type: file.folder ? "directory" : "application/octet-stream"), permissions: Permissions()))
        }
        return result
    }
    private func pending(_ item: StoredItem) -> Bool {
        let scope = model.sourceScope
        let canonical = (try? FilePath.inSource(item.file.path, scope: scope)) ?? item.file.path
        return incoming.contains { $0.path == item.file.path } ||
            activity.busySource(connection: connection.id, source: item.source, path: item.file.path, canonicalPath: canonical)
    }
    private var chosen: [StoredItem] { model.items.filter { selected.contains($0.id) } }
    var body: some View {
        browserWithActions
            .browserDownloads(downloads)
            .sheet(item: $hosting) { item in
                HostingEditor(connection: connection, item: CopyLocation(source: item.source, path: item.file.path, name: item.file.name), existing: nil)
            }
            .onReceive(model.$items) { _ in updateListing() }
            .onReceive(activity.$entries) { _ in updateListing() }
            .onReceive(activity.$jobs) { _ in updateListing() }
            .onChange(of: search) { _, _ in updateListing() }
            .onChange(of: sortOrder) { _, _ in updateListing() }
            .onDisappear { listingTask?.cancel() }
        .photosPicker(isPresented: $pickingPhotos, selection: $photos, matching: .any(of: [.images, .videos]))
        .fileImporter(isPresented: $importing, allowedContentTypes: [.data], allowsMultipleSelection: true) { result in
            Task {
                do { if let parent { await model.enqueue(urls: try result.get(), parent: parent) } }
                catch { model.error = error.localizedDescription }
            }
        }
        .onChange(of: photos) { _, picks in
            Task {
                for pick in picks {
                    do {
                        guard let file = try await pick.loadTransferable(type: ImportedMedia.self) else { throw CocoaError(.fileReadUnknown) }
                        if let parent {
                            await model.enqueue(urls: [file.url], parent: parent, scoped: false)
                            try? FileManager.default.removeItem(at: file.url.deletingLastPathComponent())
                        }
                    } catch { model.error = error.localizedDescription }
                }
                photos = []
            }
        }
        .onChange(of: model.parentProtection?.unlocked) { _, unlocked in
            if unlocked == false { trail.showSources() }
        }
        .onReceive(NotificationCenter.default.publisher(for: .fileBeansProtectionChanged)) { _ in
            model.canCreateProtectedFolder = false
            Task { await reload() }
        }
        .fullScreenCover(item: $viewer) { FolderViewer(session: $0) }
        .sheet(isPresented: $newTextFile) {
            if let parent, let repository = model.repository {
                TextFileEditor(repository: repository, parent: parent, item: nil) { Task { await reload() } }
            }
        }
        .sheet(item: $editingText) { item in
            if let parent, let repository = model.repository {
                TextFileEditor(repository: repository, parent: parent, item: item) { Task { await reload() } }
            }
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await reload() } }
        }
        .sheet(item: $archiveFolder) { item in
            ArchiveLinkView(connection: connection, folder: item) { Task { await reload() } }
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
    @ViewBuilder private var fileContent: some View {
        BrowserFileCollection(items: visible, grid: grid, header: companionLinks, row: { item, large in entry(item, large: large) })
    }
    private var companionLinks: some View { EmptyView() }
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
                    SourceTrashBar(destination: SourceDestination(connection: connection, kind: .trash(parent.source)))
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
                if scenePhase == .active { await reload() }
            }
        }
    }
    private var browserWithAlerts: some View {
        browserContent
        .alert("fileBeans", isPresented: Binding(get: { model.error != nil || activity.errors[connection.id] != nil }, set: { if !$0 { model.error = nil; activity.errors[connection.id] = nil } })) {
            Button("OK") { model.error = nil; activity.errors[connection.id] = nil }
            Button("Refresh") { Task { await reload() } }
        } message: { Text(model.error ?? activity.errors[connection.id] ?? "") }
        .alert("Rename", isPresented: Binding(get: { renameItem != nil }, set: { if !$0 { renameItem = nil; renameSelection = nil } })) {
            TextField("Name", text: $proposedName, selection: $renameSelection)
            Button("Cancel", role: .cancel) { renameItem = nil; renameSelection = nil }
            Button("Save") {
                guard let item = renameItem, let parent else { return }
                renameItem = nil
                renameSelection = nil
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
        .confirmationDialog("Delete \(chosen.count) items?", isPresented: $deleting, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                let items = chosen
                Task {
                    await model.delete(items, connection: connection, parent: parent)
                    selected.removeAll(); selecting = false
                    await reloadPreservingError()
                }
            }
        }
        .sheet(isPresented: $moving) {
            if let repository = model.repository, let parent {
                NavigationStack {
                    SourceDestinationPicker(connection: connection,
                                            mode: copying ? .copy : .localMove(connection.companionURL == nil ? parent.source : nil),
                                            excludedLocal: chosen, excludedDrive: []) { destination in
                        let items = chosen
                        let isCopy = copying
                        moving = false
                        Task {
                            copyInProgress = isCopy
                            defer { copyInProgress = false }
                            await model.transfer(items, to: destination, copying: isCopy)
                            selected.removeAll(); selecting = false
                            await reloadPreservingError()
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
                    Label("Protected folders", systemImage: "lock.fill")
                } actions: {
                    Button("Unlock") {
                        var lockedItem = item; lockedItem.protection = protection
                        changeLock(lockedItem, action: "unlock")
                    }
                }
            }
            else if displayedItems.isEmpty && model.error == nil && parent != nil {
                ContentUnavailableView("No files", systemImage: "folder")
            } else if parent == nil && sources.isEmpty || parent != nil && !model.items.isEmpty && visible.isEmpty {
                ContentUnavailableView.search(text: search)
            }
    }
    @ViewBuilder private var transferStatus: some View {
            if copyInProgress {
                HStack { ProgressView(); Text(connection.companionURL == nil ? "Copying on server…" : "Queuing server copy…") }
                    .padding().background(.regularMaterial)
            } else if let parent,
                      let upload = uploads.transfers(connection: connection.id).first(where: { $0.destination == parent.source + ": " + parent.file.path }) {
                HStack {
                    VStack(alignment: .leading) {
                        Text(upload.name).lineLimit(1)
                        if let error = upload.error {
                            Text(error).font(.caption).foregroundStyle(.red)
                            Button("Retry Upload") { UploadQueue.shared.retry(upload.id) }
                        } else { ProgressView(value: upload.progress) }
                    }
                    Button("Cancel", systemImage: "xmark") { UploadQueue.shared.cancel(upload.id) }.labelStyle(.iconOnly)
                }.padding().background(.regularMaterial)
            }
    }
    @ToolbarContentBuilder private var browserToolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .bottomBar) {
            if selecting {
                BrowserSelectionControls(host: chosen.count == 1 && chosen.allSatisfy(canHost) ? { hosting = chosen.first } : nil,
                        canDownload: !downloads.busy && !chosen.isEmpty && chosen.allSatisfy(canDownload),
                        download: { download(chosen) },
                        canCopy: !model.busy && !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.download || protected($0) || pending($0) },
                        canMove: !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.modify || pending($0) },
                        canDelete: !chosen.isEmpty && !chosen.contains { $0.isSource || !$0.permissions.delete || pending($0) },
                        copy: { copying = true; moving = true }, move: { copying = false; moving = true },
                        delete: { deleting = true }, done: { selecting = false; selected.removeAll() },
                        selectAll: { selected = Set(visible.filter { !$0.isSource && !protected($0) && !pending($0) }.map(\.id)) })
            }
        }

            ToolbarItemGroup(placement: .topBarTrailing) {
                if selecting {
                Button("Select All") { selected = Set(visible.filter { !$0.isSource && !pending($0) }.map(\.id)) }
                Button("Done") { selecting = false; selected.removeAll() }
                } else {
                    BrowserViewOptions(grid: $grid, sortOrder: $sortOrder, select: { selecting = true }, refresh: { Task { await reload() } })
                    if let parent, parent.permissions.create, model.parentProtection?.unlocked != false {
                        Menu {
                            Button("New Folder", systemImage: "folder.badge.plus") { proposedName = ""; newFolder = true }
                            Button("New Text File", systemImage: "doc.badge.plus") { newTextFile = true }
                            if connection.companionURL != nil && model.canCreateProtectedFolder && model.parentProtection == nil {
                                Button("Add Protected Folder", systemImage: "lock.fill") {
                                    ProtectedFolderDraft.shared.finish()
                                    newProtectedFolder = true
                                }
                            }
                            Button("Upload Files", systemImage: "arrow.up.doc") { importing = true }
                            Button("Upload Photos or Videos", systemImage: "photo") { pickingPhotos = true }
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
        .disabled(pending(item))
        .contextMenu {
            if canHost(item) { Button("Host File", systemImage: "link") { hosting = item } }
            if canDownload(item) {
                Button("Download", systemImage: "arrow.down.doc") { download([item]) }.disabled(downloads.busy)
            }
            if !item.file.isDirectory,
               item.permissions.download, item.permissions.modify, !protected(item) {
                Button("Edit Text", systemImage: "square.and.pencil") { editingText = item }
            }
            if item.file.isDirectory, !item.isSource, item.permissions.modify, model.canLinkArchive {
                if item.file.archiveRoot == true {
                    Button("Linked Archive…", systemImage: "link") { archiveFolder = item }
                } else if item.file.archiveLink == nil {
                    Button("Link Archive…", systemImage: "link.badge.plus") { archiveFolder = item }
                }
            }
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
                Button("Rename", systemImage: "pencil") {
                    proposedName = item.file.name
                    renameSelection = RenameText.selection(for: proposedName, isDirectory: item.file.isDirectory)
                    renameItem = item
                }
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
                        thumbnail: ThumbnailView(item: item, client: model.repository?.client), protection: item.protection, pending: pending(item), linked: item.file.archiveRoot == true)
    }
    private func changeLock(_ item: StoredItem, action: String) {
        guard let protection = item.protection else { return }
        Task {
            await model.perform {
                let client = CompanionClient(connection: connection); defer { client.close() }
                let code = action == "unlock" ? try await VaultKeySession.shared.authorizedCode(requireBackup: false) : nil
                try await client.vaultAction(id: protection.id, action: action, code: code)
                await reload()
            }
        }
    }
    private func protected(_ item: StoredItem) -> Bool {
        connection.companionURL != nil && CompanionClient.protected(item.file.path)
    }
    private func reloadPreservingError() async { let error = model.error; await reload(); if let error { model.error = error } }
    private func reload() async { await model.load(connection: connection, parent: parent) }
    private func canHost(_ item: StoredItem) -> Bool { model.canHost && canDownload(item) && item.protection == nil && model.parentProtection == nil && !protected(item) }
    private func canDownload(_ item: StoredItem) -> Bool { !item.isSource && !item.file.isDirectory && item.permissions.download && !pending(item) && item.protection?.unlocked != false }
    private func download(_ items: [StoredItem]) {
        guard let client = model.repository?.client, items.allSatisfy(canDownload) else { return }
        Task {
            do {
                try await downloads.prepare(items.map { item in
                    DownloadFile(name: item.file.name) { try await client.download(source: item.source, path: item.file.path) }
                })
            } catch { model.error = error.localizedDescription }
        }
    }
    private func open(_ item: StoredItem) {
        guard let client = model.repository?.client else { return }
        let files = visible.filter {
            !$0.file.isDirectory && $0.permissions.download && !pending($0) &&
            FolderViewerSession.canView(name: $0.file.name, media: $0.file.isMedia)
        }
        viewer = FolderViewerSession(files: files.map { file in
            FolderViewerFile(id: file.id, name: file.file.name, media: file.file.isMedia) {
                if file.file.isMedia {
                    let request = try await client.streamRequest(source: file.source, path: file.file.path)
                    return .movie(Movie(name: file.file.name,
                        stream: MediaStream(client: client, request: request, name: file.file.name)))
                }
                return .preview(try await FolderViewerSession.prepare(name: file.file.name) {
                    try await client.download(source: file.source, path: file.file.path)
                })
            }
        }, selected: item.id)

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

/// The cached movie owns playback; presentation views only display it.
@MainActor
final class Movie: ObservableObject, Identifiable {
    let id = UUID()
    let name: String
    let stream: MediaStream
    let player: AVPlayer
    @Published private(set) var playbackError: NSError?
    private var playbackObservers: [NSKeyValueObservation] = []

    init(name: String, stream: MediaStream, player: AVPlayer = AVPlayer()) {
        self.name = name; self.stream = stream; self.player = player
    }

    func playIfNeeded() async throws {
        guard player.currentItem == nil else { return }
        try await stream.play(on: player)
        guard let item = player.currentItem else { return }
        let changed: () -> Void = { [weak self, weak item] in
            Task { @MainActor in
                guard let self, let item, self.player.currentItem === item else { return }
                if item.status == .failed { self.playbackError = item.error as NSError? ?? NSError(domain: "fileBeans.Playback", code: 1) }
                self.resumeBufferedPlayback()
            }
        }
        playbackObservers = [
            player.observe(\.timeControlStatus, options: [.initial, .new]) { _, _ in changed() },
            player.observe(\.rate, options: [.new]) { _, _ in changed() },
            item.observe(\.status, options: [.initial, .new]) { _, _ in changed() },
            item.observe(\.isPlaybackBufferEmpty, options: [.new]) { _, _ in changed() },
            item.observe(\.isPlaybackLikelyToKeepUp, options: [.new]) { _, _ in changed() }
        ]
    }

    func stop() {
        playbackObservers.removeAll()
        playbackError = nil
        player.pause()
        player.replaceCurrentItem(with: nil)
        stream.cancel()
    }

    func resumeBufferedPlayback() {
        guard player.rate > 0, player.timeControlStatus == .waitingToPlayAtSpecifiedRate,
              let item = player.currentItem, item.status == .readyToPlay,
              !item.isPlaybackBufferEmpty else { return }
        player.playImmediately(atRate: player.rate)
    }
}
struct MovieView: View {
    @ObservedObject var movie: Movie
    var fullScreen = false
    @Environment(\.dismiss) private var dismiss
    private var player: AVPlayer { movie.player }
    @State private var error = false
    @State private var failureDetails = ""
    var body: some View {
        // VideoPlayer has no separate native full-screen presentation. The
        // folder viewer's single control expands this existing surface.
        VideoPlayer(player: player)
            .background(Color.black.ignoresSafeArea())
            .ignoresSafeArea(.container, edges: fullScreen ? .all : [])
            .task {
                do { try await movie.playIfNeeded() }
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
            .onReceive(movie.$playbackError) { error in
                if let error, let item = player.currentItem { showFailure(item, error: error) }
            }
            .alert("Unable to Play", isPresented: $error) { Button("OK") { dismiss() } } message: {
                Text("Playback failed.\n\n\(failureDetails)")
            }
            #if DEBUG
            .overlay(alignment: .bottomLeading) {
                if ProcessInfo.processInfo.arguments.contains("--folder-viewer-video-test") {
                    FolderVideoPlaybackProbe(movie: movie)
                }
            }
            #endif
    }
    private func showFailure(_ item: AVPlayerItem, error: NSError? = nil) {
        failureDetails = PlaybackDiagnostics.summary(
            error: error ?? item.error.map { $0 as NSError },
            mediaStatusCodes: item.errorLog()?.events.map { $0.errorStatusCode } ?? [])
        self.error = true
    }
}
