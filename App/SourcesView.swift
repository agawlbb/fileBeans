import SwiftUI

struct SourceDestination: Identifiable, Hashable {
    enum Kind: Hashable { case local(StoredItem), drive, transfers }
    let connection: Connection
    let kind: Kind
    var id: String {
        switch kind {
        case .local(let item): return connection.id.uuidString + ":local:" + item.id
        case .drive: return connection.id.uuidString + ":drive"
        case .transfers: return connection.id.uuidString + ":transfers"
        }
    }
    var name: String {
        switch kind {
        case .local(let item): return item.file.name
        case .drive: return "Google Drive"
        case .transfers: return "Transfers"
        }
    }
    @MainActor @ViewBuilder var destination: some View {
        switch kind {
        case .local(let item): BrowserView(connection: connection, parent: item)
        case .drive: DriveBrowser(connection: connection)
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
    @State private var presentationID = UUID()
    @State private var settings = false
    @State private var columnVisibility: NavigationSplitViewVisibility = .all
    @State private var compactColumn: NavigationSplitViewColumn = .sidebar
    @State private var search = ""
    @AppStorage("gridView") private var grid = false
    private var entries: [SourceDestination] {
        SourcesModel.entries(connections: connections.connections, roots: model.roots).filter {
            search.isEmpty || $0.name.localizedStandardContains(search) ||
                $0.connection.name.localizedStandardContains(search)
        }
    }
    var body: some View {
        NavigationSplitView(columnVisibility: $columnVisibility, preferredCompactColumn: $compactColumn) {
            BrowserFileCollection(items: entries, grid: grid, header: status, row: { source, large in
                Button { select(source) } label: {
                    VStack(alignment: .leading, spacing: 4) {
                        BrowserItemCell(name: source.name, size: nil, folder: true, large: large, selecting: false, selected: false,
                                        thumbnail: Image(systemName: "folder.fill").resizable().scaledToFit().padding(8).foregroundStyle(Color.accentColor))
                        if connections.connections.count > 1 {
                            Text(source.connection.name).font(.caption).foregroundStyle(.secondary)
                        }
                    }.accessibilityElement(children: .combine)
                }.buttonStyle(.plain)
            })
            .navigationTitle("fileBeans")
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
        } detail: {
            NavigationStack {
                if let selected { selected.destination }
                else { ContentUnavailableView("Choose a source", systemImage: "folder") }
            }.id(presentationID)
        }
        .onChange(of: compactColumn) { _, column in
            // The compact back button returns to the sidebar without changing
            // selected. Discard that destination's cancelled task and view state.
            if column == .sidebar { selected = nil }
        }
        .sheet(isPresented: $settings) { NavigationStack { ConnectionsSettingsView(model: connections) } }
        .task { connections.reload() }
        .task(id: connections.connections) {
            // A configuration change invalidates browsing state and cached source metadata.
            selected = nil
            compactColumn = .sidebar
            model.roots = [:]
            await model.reload(connections.connections)
        }
        .onReceive(NotificationCenter.default.publisher(for: .fileBeansConnectionChanged)) { _ in connections.reload() }
        .alert("Connections", isPresented: Binding(get: { connections.error != nil }, set: { if !$0 { connections.error = nil } })) {
            Button("OK") { connections.error = nil }
        } message: { Text(connections.error ?? "") }
    }
    private func select(_ source: SourceDestination) {
        // A source can be reopened with the same value after its task was
        // cancelled by navigation. Each opening needs a fresh detail lifetime.
        presentationID = UUID()
        selected = source; compactColumn = .detail
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
                Button { select(SourceDestination(connection: connection, kind: .transfers)) } label: {
                    Label(connections.connections.count > 1 ? "Transfers — " + connection.name : "Transfers", systemImage: "arrow.left.arrow.right")
                }
            }
        }
    }
}
