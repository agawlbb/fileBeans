import SwiftUI
import FileProvider

@main
struct MyFilesApp: App {
    var body: some Scene { WindowGroup { ConnectionsView() } }
}

@MainActor
final class ConnectionsModel: ObservableObject {
    @Published var connections: [Connection] = []
    @Published var error: String?
    func reload() {
        do { connections = try SharedStore().connections() }
        catch { self.error = error.localizedDescription }
    }
    func remove(_ connection: Connection) async {
        do {
            let domain = NSFileProviderDomain(identifier: .init(connection.id.uuidString), displayName: connection.name)
            try await NSFileProviderManager.remove(domain)
            try TokenStore.remove(connection.id)
            try SharedStore().remove(connection.id)
            reload()
        } catch { self.error = error.localizedDescription }
    }
}

struct ConnectionsView: View {
    @StateObject private var model = ConnectionsModel()
    @State private var selected: Connection?
    @State private var editing: Connection?
    @State private var adding = false
    @State private var removing: Connection?
    var body: some View {
        NavigationSplitView {
            List(selection: $selected) {
                ForEach(model.connections) { connection in
                    NavigationLink(value: connection) {
                        Label(connection.name, systemImage: "externaldrive.connected.to.line.below")
                    }
                    .contextMenu {
                        Button("Edit Connection", systemImage: "pencil") { editing = connection }
                        Button("Remove Connection", systemImage: "trash", role: .destructive) { removing = connection }
                    }
                }
            }
            .navigationTitle("myFiles")
            .toolbar { Button("Add Connection", systemImage: "plus") { adding = true } }
            .overlay {
                if model.connections.isEmpty {
                    ContentUnavailableView {
                        Label("Your files, connected", systemImage: "folder")
                    } description: { Text("Connect to File Browser Quantum.") } actions: {
                        Button("Add Connection") { adding = true }.buttonStyle(.borderedProminent)
                    }
                }
            }
        } detail: {
            if let selected {
                NavigationStack { BrowserView(connection: selected, parent: nil) }.id(selected)
            } else {
                ContentUnavailableView("Choose a connection", systemImage: "externaldrive")
            }
        }
        .task { model.reload() }
        .sheet(isPresented: $adding) { ConnectionEditor(connection: nil) { model.reload() } }
        .sheet(item: $editing) { ConnectionEditor(connection: $0) { model.reload(); selected = nil } }
        .confirmationDialog("Remove this connection and its cached files?", isPresented: Binding(
            get: { removing != nil }, set: { if !$0 { removing = nil } })) {
                Button("Remove Connection", role: .destructive) {
                    if let removing { selected = nil; Task { await model.remove(removing) } }
                }
        }
        .alert("Connection", isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
            Button("OK") { model.error = nil }
        } message: { Text(model.error ?? "") }
    }
}

struct ConnectionEditor: View {
    let connection: Connection?
    let saved: () -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var address = ""
    @State private var token = ""
    @State private var busy = false
    @State private var error: String?
    var body: some View {
        NavigationStack {
            Form {
                TextField("Name", text: $name)
                TextField("https://files.example.com", text: $address)
                    .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .accessibilityLabel("Server URL")
                SecureField(connection == nil ? "API token" : "New API token (optional)", text: $token)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                if let error { Text(error).foregroundStyle(.red) }
                if busy { ProgressView("Connecting…") }
            }
            .navigationTitle(connection == nil ? "Add Connection" : "Edit Connection")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(busy) }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(busy || name.trimmingCharacters(in: .whitespaces).isEmpty || address.isEmpty || (connection == nil && token.isEmpty))
                }
            }
            .onAppear { name = connection?.name ?? ""; address = connection?.url.absoluteString ?? "" }
        }.interactiveDismissDisabled(busy)
    }
    private func save() async {
        busy = true; defer { busy = false }
        do {
            let url = try QuantumClient.validateURL(address)
            let c = Connection(id: connection?.id ?? UUID(), name: name.trimmingCharacters(in: .whitespaces), url: url)
            // Reusing a token with a different host is deliberately prohibited.
            if let connection, connection.url != url {
                error = "Add a new connection for a different server URL."; return
            }
            let credential = try token.isEmpty ? TokenStore.read(c.id) : token.trimmingCharacters(in: .whitespacesAndNewlines)
            let store = try SharedStore()
            let repo = try Repository(connection: c, store: store, token: credential)
            _ = try await repo.client.scopes()
            let previousToken: String? = connection == nil ? nil : (try? TokenStore.read(c.id))
            try TokenStore.save(credential, for: c.id)
            let domain = NSFileProviderDomain(identifier: .init(c.id.uuidString), displayName: c.name)
            do {
                try store.save(c)
                // Adding an existing domain updates its configuration.
                try await NSFileProviderManager.add(domain)
            } catch {
                if let connection {
                    try? store.save(connection)
                    if let previousToken { try? TokenStore.save(previousToken, for: c.id) }
                    else { try? TokenStore.remove(c.id) }
                } else {
                    try? TokenStore.remove(c.id)
                    try? store.remove(c.id)
                }
                throw error
            }
            repo.notify()
            saved(); dismiss()
        } catch { self.error = error.localizedDescription }
    }
}
