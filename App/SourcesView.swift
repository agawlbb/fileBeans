import SwiftUI

struct SourceDestination: Identifiable, Hashable {
    enum Kind: Hashable { case local(StoredItem), drive, driveFolder(String, String), transfers }
    let connection: Connection
    let kind: Kind
    var id: String {
        switch kind {
        case .local(let item): return connection.id.uuidString + ":local:" + item.id
        case .drive: return connection.id.uuidString + ":drive"
        case .driveFolder(let id, _): return connection.id.uuidString + ":drive:" + id
        case .transfers: return connection.id.uuidString + ":transfers"
        }
    }
    var name: String {
        switch kind {
        case .local(let item): return item.file.name
        case .drive: return "Google Drive"
        case .driveFolder(_, let name): return name
        case .transfers: return "Transfers"
        }
    }
    @MainActor @ViewBuilder var destination: some View {
        switch kind {
        case .local(let item): BrowserView(connection: connection, parent: item)
        case .drive: DriveBrowser(connection: connection)
        case .driveFolder(let id, let name): DriveBrowser(connection: connection, folder: id, title: name)
        case .transfers: TransferList(connection: connection)
        }
    }
}

@MainActor
final class SourcesModel: ObservableObject {
    @Published var roots: [UUID: [StoredItem]] = [:]
    @Published var errors: [UUID: String] = [:]
    @Published var loading = false
    private var generation = UUID()

    static func entries(connections: [Connection], roots: [UUID: [StoredItem]]) -> [SourceDestination] {
        connections.flatMap { connection in
            var entries = (roots[connection.id] ?? []).map { SourceDestination(connection: connection, kind: .local($0)) }
            // Preserve Drive's existing setup entry, even before companion authorization.
            entries.append(SourceDestination(connection: connection, kind: .drive))
            return entries
        }.sorted {
            let order = $0.name.localizedStandardCompare($1.name)
            return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
        }
    }
    func reload(_ connections: [Connection]) async {
        let current = UUID(); generation = current; loading = true
        let ids = Set(connections.map(\.id))
        roots = roots.filter { ids.contains($0.key) }; errors = [:]
        await withTaskGroup(of: (UUID, [StoredItem]?, String?).self) { group in
            for connection in connections {
                group.addTask {
                    do {
                        let repository = try Repository(connection: connection, store: SharedStore())
                        return (connection.id, try await repository.roots(), nil)
                    } catch { return (connection.id, nil, error.localizedDescription) }
                }
            }
            for await (id, items, error) in group {
                guard generation == current, !Task.isCancelled else { continue }
                if let items { roots[id] = items }
                errors[id] = error
            }
        }
        if generation == current { loading = false }
    }
}

struct SourcesView: View {
    @StateObject private var connections = ConnectionsModel()
    @StateObject private var model = SourcesModel()
    @State private var selected: SourceDestination?
    @State private var sourcePath: [SourceDestination] = []
    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var settings = false
    @State private var columnVisibility: NavigationSplitViewVisibility = .all
    @State private var search = ""
    @AppStorage("gridView") private var grid = false
    private var entries: [SourceDestination] {
        SourcesModel.entries(connections: connections.connections, roots: model.roots).filter {
            search.isEmpty || $0.name.localizedStandardContains(search) ||
                $0.connection.name.localizedStandardContains(search)
        }
    }
    var body: some View {
        navigation
        .sheet(isPresented: $settings) { NavigationStack { ConnectionsSettingsView(model: connections) } }
        .task { connections.reload() }
        .task(id: connections.connections) {
            // A configuration change invalidates browsing state and cached source metadata.
            selected = nil
            sourcePath = []
            model.roots = [:]
            await model.reload(connections.connections)
        }
        .onReceive(NotificationCenter.default.publisher(for: .fileBeansConnectionChanged)) { _ in connections.reload() }
        .alert("Connections", isPresented: Binding(get: { connections.error != nil }, set: { if !$0 { connections.error = nil } })) {
            Button("OK") { connections.error = nil }
        } message: { Text(connections.error ?? "") }
    }
    private var navigation: some View {
        SourceNavigation(compact: sizeClass == .compact, path: $sourcePath, selected: $selected,
                         columnVisibility: $columnVisibility, sidebar: sourceList) { $0.destination }
    }
    @ViewBuilder private var sourceList: some View {
        if sizeClass != .compact, selected != nil {
            // While browsing, the sidebar is the active hierarchy. Sources
            // returns to the picker instead of duplicating that picker here.
            List { FolderBreadcrumbSidebar() }
                .listStyle(.plain)
                .navigationTitle("Sources")
                .toolbar {
                    Button("Settings", systemImage: "gearshape") { settings = true }
                }
        } else {
            sourceCollection
        }
    }
    private var sourceCollection: some View {
        BrowserFileCollection(items: entries, grid: grid, header: status, row: { source, large in
            sourceLink(source) {
                VStack(alignment: .leading, spacing: 4) {
                    BrowserItemCell(name: source.name, size: nil, folder: true, large: large, selecting: false, selected: false,
                                    thumbnail: Image(systemName: "folder.fill").resizable().scaledToFit().padding(8).foregroundStyle(Color.accentColor))
                    if connections.connections.count > 1 {
                        Text(source.connection.name).font(.caption).foregroundStyle(.secondary)
                    }
                }.accessibilityElement(children: .combine)
            }.buttonStyle(.plain)
        })
        .navigationTitle("Sources")
        .searchable(text: $search, prompt: "Search sources")
        .toolbar {
            Button("Settings", systemImage: "gearshape") { settings = true }
            Button(grid ? "List" : "Grid", systemImage: grid ? "list.bullet" : "square.grid.2x2") { grid.toggle() }
        }
        .refreshable { await model.reload(connections.connections) }
        .overlay {
            if connections.connections.isEmpty {
                ContentUnavailableView {
                    Label("Your files, connected", systemImage: "folder")
                } description: { Text("Add a connection in Settings to see your sources.") } actions: {
                    Button("Settings") { settings = true }.buttonStyle(.borderedProminent)
                }
            } else if entries.isEmpty && !model.loading && model.errors.isEmpty {
                ContentUnavailableView.search(text: search)
            }
        }
    }
    @ViewBuilder private func sourceLink<Label: View>(_ source: SourceDestination, @ViewBuilder label: () -> Label) -> some View {
        if sizeClass == .compact {
            NavigationLink(value: source, label: label)
        } else {
            Button { selected = source } label: { label() }
        }
    }
    @ViewBuilder private var status: some View {
        if model.loading { ProgressView("Loading sources…") }
        ForEach(connections.connections) { connection in
            if let error = model.errors[connection.id] {
                VStack(alignment: .leading) {
                    Text(connection.name).font(.headline)
                    Text(error).font(.caption).foregroundStyle(.secondary)
                    Button("Retry") { Task { await model.reload(connections.connections) } }
                }
            }
            if connection.companionURL != nil {
                sourceLink(SourceDestination(connection: connection, kind: .transfers)) {
                    Label(connections.connections.count > 1 ? "Transfers — " + connection.name : "Transfers", systemImage: "arrow.left.arrow.right")
                }
            }
        }
    }
}

/// Keeps the compact stack stable across source selections. Separating the host
/// also lets lifecycle tests use synthetic destinations without server access.
struct SourceNavigation<Sidebar: View, Detail: View>: View {
    let compact: Bool
    @Binding var path: [SourceDestination]
    @Binding var selected: SourceDestination?
    @Binding var columnVisibility: NavigationSplitViewVisibility
    let sidebar: Sidebar
    @State private var detailPath: [SourceDestination] = []
    @ViewBuilder let detail: (SourceDestination) -> Detail

    var body: some View {
        if compact {
            NavigationStack(path: $path) {
                sidebar.navigationDestination(for: SourceDestination.self) { detail($0) }
            }
            .environment(\.folderBreadcrumbs, FolderBreadcrumbContext(locations: path,
                navigate: { path = FolderBreadcrumbContext.prefix(path, through: $0) },
                showSources: { path = [] }))
        } else {
            NavigationSplitView(columnVisibility: $columnVisibility) {
                sidebar
            } detail: {
                NavigationStack(path: $detailPath) {
                    Group {
                        if let selected { detail(selected) }
                        else { ContentUnavailableView("Choose a source", systemImage: "folder") }
                    }
                    .navigationDestination(for: SourceDestination.self) { detail($0) }
                }.id(selected)
            }
            .environment(\.folderBreadcrumbs, FolderBreadcrumbContext(
                locations: selected.map { [$0] + detailPath } ?? [],
                navigate: { index in
                    guard index >= 0, index <= detailPath.count else { return }
                    detailPath = Array(detailPath.prefix(index))
                },
                showSources: { detailPath = []; selected = nil }))
            .onChange(of: selected) { _, _ in detailPath = [] }
        }
    }
}
