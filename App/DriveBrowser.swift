import SwiftUI
import QuickLook
import PhotosUI

@MainActor
final class DriveBrowserModel: ObservableObject {
    @Published var files: [DriveFile] = []
    @Published var current: DriveFile?
    @Published var connected = false
    @Published var capabilitiesLoaded = false
    @Published var writable = false
    @Published var modern = false
    @Published var normalizationEnabled = false
    @Published var busy = false
    @Published var error: String?
    @Published var next = ""
    private(set) var client: DriveClient?
    private var pages = 1
    func load(connection: Connection, folder: String, page: String = "", refreshLoaded: Bool = false) async {
        guard !busy, connection.companionURL != nil else { return }
        busy = true; defer { busy = false }
        do {
            if client?.transport.connection != connection { client?.close(); client = DriveClient(connection: connection) }
            guard let client else { return }
            let capabilities = try await client.transport.capabilities()
            capabilitiesLoaded = true
            connected = capabilities.driveConnected
            writable = capabilities.driveWriteAccess == true
            normalizationEnabled = capabilities.normalizationEnabled == true
            modern = capabilities.features?.contains("driveWrite") == true
            guard connected else { files = []; next = ""; current = nil; return }
            let transferJobs = try? await client.transport.jobs()
            var loaded: [DriveFile] = []
            if folder == "__drives" {
                let result = try await client.drives(page: page)
                loaded = result.drives.map { DriveFile(id: $0.id, name: $0.name, mimeType: "application/vnd.google-apps.folder", sourceRoot: true) }
                next = result.nextPageToken ?? ""
                current = nil
            } else {
                if modern && folder != "shared" { current = try await client.file(folder) } else { current = nil }
                var cursor = page
                let count = refreshLoaded ? pages : 1
                for _ in 0..<count {
                    let result = try await client.list(folder: folder, page: cursor)
                    loaded += result.files
                    cursor = result.nextPageToken ?? ""
                    if cursor.isEmpty { break }
                }
                next = cursor
            }
            if page.isEmpty { files = loaded; if !refreshLoaded { pages = 1 } }
            else { let known = Set(files.map(\.id)); files += loaded.filter { !known.contains($0.id) }; pages += 1 }
            if let jobs = transferJobs { FileActivity.shared.synchronize(jobs, connection: connection.id) }
            error = nil
        } catch {
            guard !Task.isCancelled, !(error is CancellationError),
                  (error as? URLError)?.code != .cancelled else { return }
            self.error = error.localizedDescription
        }
    }
    func perform(_ work: () async throws -> Void) async -> Bool {
        guard !busy else { return false }; busy = true; defer { busy = false }
        do { try await work(); error = nil; return true } catch { self.error = error.localizedDescription; return false }
    }
    deinit { client?.close() }
}
struct DriveBrowser: View {
    let connection: Connection
    var folder = "root"
    var title = "Google Drive"
    @StateObject private var model = DriveBrowserModel()
    @ObservedObject private var activity = FileActivity.shared
    @AppStorage("gridView") private var grid = false
    @AppStorage("sortOrder") private var sortOrder = "name"
    @State private var search = ""
    @State private var selecting = false
    @State private var selected = Set<String>()
    @State private var renaming: DriveFile?
    @State private var proposedName = ""
    @State private var newFolder = false
    @State private var deleting = false
    @State private var choosingDestination = false
    @State private var copying = true
    @State private var importing = false
    @State private var photos: [PhotosPickerItem] = []
    @State private var preview: URL?
    @State private var movie: Movie?
    @State private var configuring = false
    @State private var disconnecting = false
    @State private var normalizing: DriveFile?
    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var phase
    private var queue: DriveUploadQueue { DriveUploadQueue.shared(connection: connection) }
    private var chosen: [DriveFile] { model.files.filter { selected.contains($0.id) } }
    private var visible: [DriveFile] {
        displayedFiles.filter { search.isEmpty || $0.name.localizedStandardContains(search) }
            .sorted { BrowserOrdering.precedes($0.sortValue, $1.sortValue, order: sortOrder) }
    }

    private var incoming: [ActivityFile] {
        activity.pending(connection: connection.id, source: "drive", parent: folder, canonicalParent: folder)
    }
    private var displayedFiles: [DriveFile] {
        model.files + incoming.filter { incoming in !model.files.contains { $0.name == incoming.name } }.map {
            DriveFile(id: $0.path, name: $0.name, mimeType: $0.folder ? "application/vnd.google-apps.folder" : "application/octet-stream")
        }
    }
    private func pending(_ item: DriveFile) -> Bool {
        incoming.contains { $0.name == item.name } || activity.busySource(connection: connection.id, source: "drive", path: item.id, canonicalPath: item.id)
    }
    var body: some View {
        browserWithActions
            .fileImporter(isPresented: $importing, allowedContentTypes: [.data], allowsMultipleSelection: true) { result in
                do { if let parent = model.current { queue.enqueue(try result.get(), parent: parent.id) } }
                catch { model.error = error.localizedDescription }
            }
            .onChange(of: photos) { _, picks in
                Task {
                    for pick in picks {
                        do {
                            if let file = try await pick.loadTransferable(type: ImportedMedia.self), let parent = model.current {
                                queue.enqueue([file.url], parent: parent.id, scoped: false)
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
    private var browser: some View {
        BrowserFileCollection(items: visible, grid: grid, header: header, row: { item, large in entry(item, large: large) },
                              loadMore: model.next.isEmpty ? nil : { Task { await reload(page: model.next) } })
            // The hierarchy replaces the large folder heading below the controls.
            .navigationTitle("")
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .top, spacing: 0) {
                FolderBreadcrumbHeading(fallbackTitle: title)
            }
            .searchable(text: $search, prompt: "Search this folder")
            .toolbar { toolbar }
            .overlay { if model.busy && model.files.isEmpty { ProgressView() } }
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 0) {
                    DriveUploadStatus(queue: queue)
                    if folder == "root", model.connected, model.modern {
                        SourceTrashBar(destination: DriveTrashView(connection: connection))
                    }
                }
            }
            .refreshable { await reload(refreshLoaded: true) }
            .task(id: phase) {
                guard phase == .active else { return }
                queue.recover()
                await reload(refreshLoaded: true)
                guard let client = model.client else { return }
                var finished = Set((try? await client.transport.jobs())?.filter { $0.state == "completed" }.map(\.id) ?? [])
                while !Task.isCancelled {
                    do { try await Task.sleep(for: .seconds(5)) } catch { return }
                    guard let jobs = try? await client.transport.jobs() else { continue }
                    let current = Set(jobs.filter { $0.state == "completed" }.map(\.id))
                    if current != finished { finished = current; await reload(refreshLoaded: true) }
                    activity.synchronize(jobs, connection: connection.id)
                }
            }
    }
    @ViewBuilder private var header: some View {
        if connection.companionURL == nil {
            ContentUnavailableView {
                Label("Set Up Google Drive", systemImage: "externaldrive")
            } description: { Text("Add this connection’s companion URL and token to enable Google Drive.") } actions: { Button("Set Up Companion") { configuring = true } }
        } else if !model.capabilitiesLoaded {
            if let error = model.error {
                VStack(alignment: .leading, spacing: 8) {
                    Text(error).font(.callout)
                    Button("Retry") { Task { await reload() } }
                }
            } else {
                ProgressView("Checking Google Drive…")
            }
        } else if !model.connected {
            ContentUnavailableView {
                Label("Connect Google Drive", systemImage: "externaldrive")
            } description: { Text("Sign in to the Google account you want to browse and manage.") } actions: { Button("Connect Drive") { connect() } }
        } else {
            if !model.writable {
                VStack(alignment: .leading, spacing: 8) {
                    Text(model.modern ? "Drive is connected with read-only access." : "Update the companion service to enable Drive file management.")
                    if model.modern { Button("Upgrade Drive Access") { connect() } }
                }.font(.callout)
            }
            if folder == "root" {
                NavigationLink("Shared with me", value: SourceDestination(connection: connection, kind: .driveFolder("shared", "Shared with me")))
                NavigationLink("Shared drives", value: SourceDestination(connection: connection, kind: .driveFolder("__drives", "Shared drives")))
            }
            if model.files.isEmpty && !model.busy { Text("No files").foregroundStyle(.secondary) }
        }
    }
    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .bottomBar) {
            if selecting {
                BrowserSelectionControls(canCopy: !chosen.isEmpty && !chosen.contains(where: pending) && chosen.allSatisfy(\.canCopy),
                                         canMove: model.writable && !chosen.isEmpty && !chosen.contains(where: pending) && chosen.allSatisfy(\.canMove),
                                         canDelete: model.writable && !chosen.isEmpty && !chosen.contains(where: pending) && chosen.allSatisfy(\.canTrash),
                                         copy: { copying = true; choosingDestination = true },
                                         move: { copying = false; choosingDestination = true },
                                         delete: { deleting = true }, done: { selecting = false; selected.removeAll() },
                                         selectAll: { selected = Set(visible.filter { !pending($0) && $0.sourceRoot != true }.map(\.id)) })
                    .disabled(model.busy)
            }
        }

        ToolbarItem(placement: .topBarLeading) {
            if connection.companionURL != nil { NavigationLink { TransferList(connection: connection) } label: { Label("Transfers", systemImage: "arrow.left.arrow.right") } }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            if selecting {
                Button("Select All") { selected = Set(visible.filter { !pending($0) }.map(\.id)) }
                Button("Done") { selecting = false; selected.removeAll() }
            } else {
                BrowserViewOptions(grid: $grid, sortOrder: $sortOrder, select: { selecting = true }, refresh: { Task { await reload(refreshLoaded: true) } })
                if model.writable && model.current?.capabilities?.canAddChildren == true {
                    Menu {
                        Button("New Folder", systemImage: "folder.badge.plus") { proposedName = ""; newFolder = true }
                        Button("Upload Files", systemImage: "arrow.up.doc") { importing = true }
                        PhotosPicker(selection: $photos, matching: .any(of: [.images, .videos])) { Label("Upload Photos or Videos", systemImage: "photo") }
                    } label: { Label("Add", systemImage: "plus") }
                }
                if folder == "root" && connection.companionURL != nil {
                    Menu {
                        Button(model.writable ? "Reconnect Drive" : model.connected ? "Upgrade Drive Access" : "Connect Drive") { connect() }
                        if model.connected { Button("Disconnect Drive", role: .destructive) { disconnecting = true } }
                    } label: { Label("Drive Connection", systemImage: "person.crop.circle") }
                }
            }
        }
    }
    private var browserWithAlerts: some View {
        browser
            .sheet(isPresented: $configuring) { ConnectionEditor(connection: connection) {} }
            .alert("Google Drive", isPresented: Binding(get: { model.error != nil || activity.errors[connection.id] != nil }, set: { if !$0 { model.error = nil; activity.errors[connection.id] = nil } })) {
                Button("OK") { model.error = nil; activity.errors[connection.id] = nil }
            } message: { Text(model.error ?? activity.errors[connection.id] ?? "") }
            .alert("Rename", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
                TextField("Name", text: $proposedName)
                Button("Cancel", role: .cancel) { renaming = nil }
                Button("Save") {
                    guard let item = renaming else { return }; renaming = nil
                    mutate(DriveMutationRequest(action: "rename", fileId: item.id, name: proposedName, version: item.version))
                }
            }
            .alert("New Folder", isPresented: $newFolder) {
                TextField("Name", text: $proposedName)
                Button("Cancel", role: .cancel) {}
                Button("Create") { if let current = model.current { mutate(DriveMutationRequest(action: "folder", parent: current.id, name: proposedName)) } }
            }
    }
    private var browserWithConfirmations: some View {
        browserWithAlerts
            .confirmationDialog("Delete \(chosen.count) items?", isPresented: $deleting, titleVisibility: .visible) {
                Button("Move to Trash", role: .destructive) {
                    let items = chosen
                    Task {
                        guard let client = model.client else { return }
                        let ids = activity.begin(items.map { ActivityFile(source: "drive", path: $0.id, name: $0.name, folder: $0.folder) },
                                                 connection: connection.id, operation: "delete")
                        var failures: [String] = []
                        for (i,item) in items.enumerated() {
                            do { try await client.mutate(DriveMutationRequest(action: "trash", fileId: item.id, version: item.version)); model.files.removeAll { $0.id == item.id } }
                            catch { failures.append(item.name + ": " + error.localizedDescription) }
                            activity.finish([ids[i]])
                        }
                        selected.removeAll(); selecting = false; await reload(refreshLoaded: true)
                        if !failures.isEmpty { model.error = failures.joined(separator: "\n") }
                    }
                }
            }
            .confirmationDialog("Import and normalize this item into VPS pics? Drive files remain unchanged.", isPresented: Binding(get: { normalizing != nil }, set: { if !$0 { normalizing = nil } }), titleVisibility: .visible) {
                if let item = normalizing { Button("Import to VPS pics") {
                    normalizing = nil
                    Task {
                        let client = NormalizationClient(connection: connection); defer { client.close() }
                        _ = await model.perform { try await client.submit(.init(source: "drive", path: item.id)) }
                    }
                } }
            }
            .confirmationDialog("Disconnect Drive? Existing transfers remain tied to this account and may need reconnection or cancellation.", isPresented: $disconnecting, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) {
                    Task {
                        let success = await model.perform { _ = try await model.client?.transport.request("drive/disconnect", method: "POST") }
                        if success { await reload() }
                    }
                }
            }
    }
    private var browserWithActions: some View {
        browserWithConfirmations
            .sheet(isPresented: $choosingDestination) {
                NavigationStack {
                    SourceDestinationPicker(connection: connection, mode: copying ? .copy : .driveMove,
                                            excludedLocal: [], excludedDrive: Set(chosen.filter(\.folder).map(\.id))) { destination in
                        let items = chosen; let isCopy = copying; choosingDestination = false
                        Task {
                            guard let client = model.client else { return }
                            let target: CopyLocation
                            switch destination { case .local(let item): target = CopyLocation(source: item.source, path: item.file.path)
                            case .drive(let item): target = CopyLocation(source: "drive", path: item.id, name: item.name) }
                            let ids = activity.begin(items.map { ActivityFile(source: "drive", path: $0.id, name: $0.name, folder: $0.folder) },
                                connection: connection.id, operation: isCopy ? "copy" : "move", destination: target)
                            var failures: [String] = []
                            if isCopy {
                                do {
                                    guard target.source == "drive" || !items.contains(where: \.nativeDocument) else { throw APIError.forbidden }
                                    let job = try await client.transport.submit(BackgroundCopy(items: items.map { CopyLocation(source: "drive", path: $0.id, name: $0.name) }, destination: target))
                                    activity.attach(job, ids: ids, connection: connection.id)
                                } catch { activity.finish(ids); failures.append(error.localizedDescription) }
                            } else {
                                for (i,item) in items.enumerated() {
                                    do {
                                        guard case .drive(let folder) = destination else { throw APIError.forbidden }
                                        try await client.mutate(DriveMutationRequest(action: "move", fileId: item.id, parent: folder.id, version: item.version))
                                        model.files.removeAll { $0.id == item.id }
                                    } catch { failures.append(item.name + ": " + error.localizedDescription) }
                                    activity.finish([ids[i]])
                                }
                            }
                            selected.removeAll(); selecting = false; await reload(refreshLoaded: true)
                            if !failures.isEmpty { model.error = failures.joined(separator: "\n") }
                        }
                    }.toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { choosingDestination = false } } }
                }
            }
    }
    @ViewBuilder private func entry(_ item: DriveFile, large: Bool) -> some View {
        Group {
            if item.folder && !selecting {
                NavigationLink(value: SourceDestination(connection: connection, kind: .driveFolder(item.id, item.name))) { cell(item, large: large) }
            } else {
                Button {
                    if selecting { if selected.contains(item.id) { selected.remove(item.id) } else { selected.insert(item.id) } }
                    else { open(item) }
                } label: { cell(item, large: large) }.buttonStyle(.plain)
            }
        }
        .contextMenu {
            if model.normalizationEnabled && (item.folder || item.downloadable) {
                Button("Normalize to VPS pics", systemImage: "film.stack") { normalizing = item }
            }
            if item.canCopy { Button("Copy", systemImage: "doc.on.doc") { selected = [item.id]; copying = true; choosingDestination = true } }
            if model.writable && item.canRename { Button("Rename", systemImage: "pencil") { renaming = item; proposedName = item.name } }
            if model.writable && item.canMove { Button("Move", systemImage: "folder") { selected = [item.id]; copying = false; choosingDestination = true } }
            if !item.folder { Button("Open", systemImage: "arrow.up.right.square") { open(item) } }
            if model.writable && item.canTrash { Button("Delete", systemImage: "trash", role: .destructive) { selected = [item.id]; deleting = true } }
        }.disabled(model.busy || pending(item))
    }
    private func cell(_ item: DriveFile, large: Bool) -> some View {
        BrowserItemCell(name: item.name, size: item.size.flatMap(Int64.init), folder: item.folder, large: large,
                        selecting: selecting, selected: selected.contains(item.id),
                        thumbnail: DriveThumbnailView(connection: connection, item: item, enabled: model.modern && !item.id.hasPrefix("pending:")), pending: pending(item))
    }
    private func reload(page: String = "", refreshLoaded: Bool = false) async { await model.load(connection: connection, folder: folder, page: page, refreshLoaded: refreshLoaded) }
    private func connect() {
        Task {
            if model.client == nil { await reload() }
            guard let client = model.client else { return }
            _ = await model.perform { openURL(try await client.connect()) }
        }
    }
    private func mutate(_ request: DriveMutationRequest) {
        Task {
            guard let client = model.client else { return }
            if await model.perform({ try await client.mutate(request) }) { await reload(refreshLoaded: true) }
        }
    }
    private func open(_ item: DriveFile) {
        guard let client = model.client else { return }
        Task {
            _ = await model.perform {
                if item.nativeDocument {
                    let address = item.webViewLink.flatMap(URL.init(string:)) ?? URL(string: "https://drive.google.com/file/d/" + item.id + "/view")!
                    guard address.scheme == "https", let host = address.host, host == "google.com" || host.hasSuffix(".google.com") else { throw APIError.invalidURL }
                    openURL(address)
                } else if item.media {
                    let request = try client.transport.authorizedRequest("drive/files/" + item.id + "/content")
                    movie = Movie(name: item.name, stream: MediaStream(client: client.transport, request: request, name: item.name))
                } else if item.downloadable {
                    _ = try FilePath.child(item.name, in: "/")
                    let file = try await client.transport.download("drive/files/" + item.id + "/content")
                    let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
                    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                    let target = directory.appendingPathComponent(item.name)
                    try FileManager.default.moveItem(at: file, to: target); preview = target
                } else { throw CocoaError(.featureUnsupported) }
            }
        }
    }
}
struct DriveThumbnailView: View {
    let connection: Connection
    let item: DriveFile
    let enabled: Bool
    @State private var image: UIImage?
    private var key: ThumbnailKey {
        ThumbnailKey(connection: connection.id, provider: "drive", source: item.driveId ?? "",
                     file: item.id, revision: item.version ?? item.modifiedTime ?? "")
    }
    var body: some View {
        Group {
            if !item.folder, let image { Image(uiImage: image).resizable().scaledToFit().clipShape(RoundedRectangle(cornerRadius: 6)) }
            else { Image(systemName: item.folder ? "folder.fill" : item.media ? "play.rectangle" : "doc").resizable().scaledToFit().padding(8).foregroundStyle(item.folder ? Color.accentColor : .secondary) }
        }
        .accessibilityHidden(true)
        .task(id: key) {
            image = nil
            guard enabled, !item.folder, item.hasThumbnail == true else { return }
            do {
                let data = try await ThumbnailCache.shared.data(for: key) {
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    return try await client.request("drive/files/" + item.id + "/thumbnail")
                }
                try Task.checkCancellation()
                image = UIImage(data: data)
            } catch { /* Keep the file-type icon. */ }
        }
    }
}
struct DriveUploadStatus: View {
    @ObservedObject var queue: DriveUploadQueue
    var body: some View {
        if queue.busy {
            HStack {
                VStack(alignment: .leading) { Text(queue.name).lineLimit(1); Text(queue.phase).font(.caption); ProgressView(value: queue.progress) }
                Button("Cancel", systemImage: "xmark") { queue.cancel() }.labelStyle(.iconOnly)
            }.padding().background(.regularMaterial)
        } else if queue.canRetry {
            VStack { if let error = queue.error { Text(error).font(.caption).foregroundStyle(.red) }; Button("Retry Upload") { queue.retry() } }.padding().background(.regularMaterial)
        } else if let error = queue.error { Text(error).font(.caption).foregroundStyle(.red).padding() }
    }
}
