import SwiftUI

struct SourceDestination: Identifiable, Hashable {
    enum Kind: Hashable { case local(StoredItem), drive, driveFolder(String, String), transfers, protection, trash(String), driveTrash, normalization }
    let connection: Connection
    let kind: Kind
    var id: String {
        switch kind {
        case .local(let item): return connection.id.uuidString + ":local:" + item.id
        case .drive: return connection.id.uuidString + ":drive"
        case .driveFolder(let id, _): return connection.id.uuidString + ":drive:" + id
        case .transfers: return connection.id.uuidString + ":transfers"
        case .protection: return connection.id.uuidString + ":protection"
        case .trash(let source): return connection.id.uuidString + ":trash:" + source
        case .driveTrash: return connection.id.uuidString + ":driveTrash"
        case .normalization: return connection.id.uuidString + ":normalization"
        }
    }
    var name: String {
        switch kind {
        case .local(let item): return item.file.name
        case .drive: return "Google Drive"
        case .driveFolder(_, let name): return name
        case .transfers: return "Transfers"
        case .protection: return "Protected folders"
        case .trash, .driveTrash: return "Trash"
        case .normalization: return "Media Normalization"
        }
    }
    @MainActor @ViewBuilder var destination: some View {
        switch kind {
        case .local(let item): BrowserView(connection: connection, parent: item)
        case .drive: DriveBrowser(connection: connection)
        case .driveFolder(let id, let name): DriveBrowser(connection: connection, folder: id, title: name)
        case .transfers: TransferList(connection: connection)
        case .protection: VaultsView(connection: connection)
        case .trash(let source): TrashList(connection: connection, source: source)
        case .driveTrash: DriveTrashView(connection: connection)
        case .normalization: NormalizationView(connection: connection)
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
    @StateObject private var routeState = SourceNavigationState()
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
                         columnVisibility: $columnVisibility, sidebar: sourceList,
                         overview: AnyView(sourceCollection), transfers: AnyView(transfersButton), state: routeState) { $0.destination }
    }
    @ViewBuilder private var sourceList: some View {
        if sizeClass != .compact {
            // While browsing, the sidebar is the active hierarchy. Sources
            // returns to the picker instead of duplicating that picker here.
            List {
                if selected != nil { FolderBreadcrumbSidebar() }
                else { Label("Sources", systemImage: "externaldrive") }
            }
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
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Settings", systemImage: "gearshape") { settings = true }
                Button(grid ? "List" : "Grid", systemImage: grid ? "list.bullet" : "square.grid.2x2") { grid.toggle() }
            }
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
    @ViewBuilder private var transfersButton: some View {
        let destination = sizeClass == .compact ? sourcePath.last : routeState.path.last ?? selected
        let active = destination?.connection
        let available = active.map { [$0] } ?? connections.connections.filter { $0.companionURL != nil }
        if available.count == 1, let connection = available.first {
            if sizeClass != .compact && selected == nil {
                Button { selected = SourceDestination(connection: connection, kind: .transfers) } label: {
                    Label("Transfers", systemImage: "arrow.left.arrow.right")
                }.accessibilityIdentifier("transfers")
            } else {
                NavigationLink(value: SourceDestination(connection: connection, kind: .transfers)) {
                    Label("Transfers", systemImage: "arrow.left.arrow.right")
                }.accessibilityIdentifier("transfers").disabled(destination?.kind == .transfers)
            }
        } else if !available.isEmpty {
            Menu {
                ForEach(available) { connection in
                    sourceLink(SourceDestination(connection: connection, kind: .transfers)) { Text(connection.name) }
                }
            } label: { Label("Transfers", systemImage: "arrow.left.arrow.right") }
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
        }
    }
}

@MainActor
final class SourceNavigationState: ObservableObject {
    @Published var path: [SourceDestination] = []
    @Published var identity = UUID()
}

/// Keeps the compact stack stable across source selections. Separating the host
/// also lets lifecycle tests use synthetic destinations without server access.
struct SourceNavigation<Sidebar: View, Detail: View>: View {
    let compact: Bool
    @Binding var path: [SourceDestination]
    @Binding var selected: SourceDestination?
    @Binding var columnVisibility: NavigationSplitViewVisibility
    let sidebar: Sidebar
    var overview: AnyView? = nil
    var transfers: AnyView? = nil
    @StateObject var state = SourceNavigationState()
    @ViewBuilder let detail: (SourceDestination) -> Detail

    var body: some View {
        if compact {
            NavigationStack(path: $path) {
                sidebar.navigationDestination(for: SourceDestination.self) { detail($0) }
            }
            .id(state.identity)
            .toolbar { ToolbarItem(placement: .topBarLeading) { transfers } }
            .environment(\.folderBreadcrumbs, FolderBreadcrumbContext(locations: path,
                navigate: { path = FolderBreadcrumbContext.prefix(path, through: $0); state.identity = UUID() },
                showSources: { path = []; state.identity = UUID() }))
        } else {
            NavigationSplitView(columnVisibility: $columnVisibility) {
                sidebar
            } detail: {
                NavigationStack(path: $state.path) {
                    Group {
                        if let selected { detail(selected) }
                        else if let overview { overview }
                        else { ContentUnavailableView("Choose a source", systemImage: "folder") }
                    }
                    .navigationDestination(for: SourceDestination.self) { detail($0) }
                }.id(state.identity)
                .toolbar { ToolbarItem(placement: .topBarLeading) { transfers } }
            }
            .environment(\.folderBreadcrumbs, FolderBreadcrumbContext(
                locations: selected.map { [$0] + state.path } ?? [],
                navigate: { index in
                    guard index >= 0, index <= state.path.count else { return }
                    state.path = Array(state.path.prefix(index))
                    state.identity = UUID()
                },
                showSources: { state.path = []; selected = nil; state.identity = UUID() }))
            .onChange(of: selected) { _, _ in state.path = []; state.identity = UUID() }
        }
    }
}
