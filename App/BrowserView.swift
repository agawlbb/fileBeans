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
    @State private var deleting = false
    @State private var moving = false
    @State private var copying = false
    @State private var copyInProgress = false
    @State private var importing = false
    @State private var preview: URL?
    @State private var movie: Movie?
    @State private var photos: [PhotosPickerItem] = []
    private var visible: [StoredItem] {
        model.items.filter { search.isEmpty || $0.file.name.localizedStandardContains(search) }.sorted {
            if $0.file.isDirectory != $1.file.isDirectory { return $0.file.isDirectory }
            if sortOrder == "modified" { return ($0.file.date ?? .distantPast) > ($1.file.date ?? .distantPast) }
            if sortOrder == "size" { return ($0.file.size ?? 0) > ($1.file.size ?? 0) }
            return $0.file.name.localizedStandardCompare($1.file.name) == .orderedAscending
        }
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
    }
    private var fileContent: some View {
        Group {
            if grid {
                ScrollView {
                    companionLinks.padding(.horizontal)
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 140))], spacing: 24) {
                        ForEach(visible) { item in entry(item, large: true) }
                    }.padding()
                }
            } else {
                List {
                    companionLinks
                    ForEach(visible) { item in entry(item, large: false) }
                }
                    .listStyle(.plain)
            }
        }
    }
    @ViewBuilder private var companionLinks: some View {
        if parent == nil && connection.companionURL != nil {
            NavigationLink { DriveBrowser(connection: connection) } label: { Label("Google Drive", systemImage: "externaldrive") }
            NavigationLink { TransferList(connection: connection) } label: { Label("Transfers", systemImage: "arrow.left.arrow.right") }
            NavigationLink { TrashList(connection: connection) } label: { Label("Trash", systemImage: "trash") }
            Text("Deletion protection: 10 days").font(.caption).foregroundStyle(.secondary)
        }
    }
    private var browserContent: some View {
        fileContent
        .navigationTitle(parent?.file.name ?? connection.name)
        .searchable(text: $search, prompt: "Search this folder")
        .overlay { emptyState }
        .safeAreaInset(edge: .bottom) { transferStatus }
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
                    FolderDestination(repository: repository, folder: nil, source: parent.source, excluded: chosen, copying: copying) { destination in
                        let items = chosen
                        let isCopy = copying
                        moving = false
                        Task {
                            copyInProgress = isCopy
                            defer { copyInProgress = false }
                            let succeeded = await model.perform {
                                if isCopy { try await repository.copy(items, to: destination) }
                                else { for item in items { try await repository.move(item, to: destination, name: item.file.name) } }
                            }
                            if succeeded { selected.removeAll(); selecting = false; await reload() }
                        }
                    }.toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { moving = false } } }
                }
            }
        }
    }
    @ViewBuilder private var emptyState: some View {
            if model.busy && model.items.isEmpty { ProgressView() }
            else if model.items.isEmpty && model.error == nil {
                ContentUnavailableView("No files", systemImage: "folder")
            } else if !model.items.isEmpty && visible.isEmpty {
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
                    Button("Copy", systemImage: "doc.on.doc") { copying = true; moving = true }
                        .disabled(model.busy || chosen.isEmpty || chosen.contains { $0.isSource || !$0.permissions.download || protected($0) })
                    Button("Move", systemImage: "folder") { copying = false; moving = true }
                        .disabled(chosen.isEmpty || chosen.contains { $0.isSource || !$0.permissions.modify })
                    Button("Delete", systemImage: "trash", role: .destructive) { deleting = true }
                        .disabled(chosen.isEmpty || chosen.contains { $0.isSource || !$0.permissions.delete })
                    Button("Done") { selecting = false; selected.removeAll() }
                } else {
                    Menu {
                        Button(grid ? "List" : "Grid", systemImage: grid ? "list.bullet" : "square.grid.2x2") { grid.toggle() }
                        Picker("Sort", selection: $sortOrder) {
                            Text("Name").tag("name"); Text("Modified").tag("modified"); Text("Size").tag("size")
                        }
                        Button("Select", systemImage: "checkmark.circle") { selecting = true }
                        Button("Refresh", systemImage: "arrow.clockwise") { Task { await reload() } }
                    } label: { Label("View Options", systemImage: "ellipsis.circle") }
                    if let parent, parent.permissions.create {
                        Menu {
                            Button("New Folder", systemImage: "folder.badge.plus") { proposedName = ""; newFolder = true }
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
            if item.file.isDirectory && !selecting && connection.companionURL != nil && item.file.path == "/Trash" {
                NavigationLink { TrashList(connection: connection, source: item.source) } label: { cell(item, large: large) }
            } else if item.file.isDirectory && !selecting {
                NavigationLink { BrowserView(connection: connection, parent: item) } label: { cell(item, large: large) }
            } else {
                Button {
                    if selecting {
                        if selected.contains(item.id) { selected.remove(item.id) } else { selected.insert(item.id) }
                    } else { open(item) }
                } label: { cell(item, large: large) }.buttonStyle(.plain)
            }
        }
        .contextMenu {
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
    @ViewBuilder private func cell(_ item: StoredItem, large: Bool) -> some View {
        let layout = large ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 12))
        layout {
            ThumbnailView(item: item, client: model.repository?.client)
                .frame(width: large ? 120 : 44, height: large ? 100 : 44)
            VStack(alignment: .leading, spacing: 4) {
                Text(item.file.name).foregroundStyle(.primary).lineLimit(2)
                if !item.file.isDirectory, let size = item.file.size {
                    Text(ByteCountFormatter.string(fromByteCount: size, countStyle: .file))
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            if selecting { Image(systemName: selected.contains(item.id) ? "checkmark.circle.fill" : "circle").foregroundStyle(.tint) }
        }
        .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected.contains(item.id) ? .isSelected : [])
    }
    private func protected(_ item: StoredItem) -> Bool { connection.companionURL != nil && CompanionClient.protected(item.file.path) }
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

private struct ImportedMedia: Transferable {
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

private struct Movie: Identifiable { let id = UUID(); let name: String; let stream: MediaStream }
private struct MovieView: View {
    let movie: Movie
    @Environment(\.dismiss) private var dismiss
    @State private var player = AVPlayer()
    @State private var error = false
    @State private var failureDetails = ""
    var body: some View {
        NavigationStack {
            VideoPlayer(player: player)
                .navigationTitle(movie.name).navigationBarTitleDisplayMode(.inline)
                .toolbar { Button("Done") { dismiss() } }
                .task {
                    do { try await movie.stream.play(on: player) }
                    catch {
                        guard !Task.isCancelled else { return }
                        failureDetails = PlaybackDiagnostics.summary(error: error as NSError)
                        self.error = true
                    }
                }
                .onDisappear { player.pause(); player.replaceCurrentItem(with: nil); movie.stream.cancel() }
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
