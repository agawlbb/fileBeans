import SwiftUI
import FileProvider

@main
struct MyFilesApp: App {
    var body: some Scene { WindowGroup { SourcesView() } }
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
            if let id = connection.companionTokenID { try TokenStore.remove(id) }
            await ThumbnailCache.shared.clear(connection: connection.id)
            try SharedStore().remove(connection.id)
            reload()
        } catch { self.error = error.localizedDescription }
    }
}

struct ConnectionsSettingsView: View {
    @ObservedObject var model: ConnectionsModel
    @Environment(\.dismiss) private var dismiss
    @State private var editing: Connection?
    @State private var adding = false
    @State private var removing: Connection?
    var body: some View {
        List {
            Section("Connections") {
                ForEach(model.connections) { connection in
                    HStack {
                        Button { editing = connection } label: {
                            Label(connection.name, systemImage: "externaldrive.connected.to.line.below")
                        }
                        Spacer()
                        Button("Remove", systemImage: "trash", role: .destructive) { removing = connection }
                            .labelStyle(.iconOnly).buttonStyle(.borderless)
                    }
                    .contextMenu {
                        Button("Edit Connection", systemImage: "pencil") { editing = connection }
                        Button("Remove Connection", systemImage: "trash", role: .destructive) { removing = connection }
                    }
                }
                Button("Add Connection", systemImage: "plus") { adding = true }
            }
        }
        .navigationTitle("Settings")
        .toolbar { Button("Done") { dismiss() } }
        .sheet(isPresented: $adding) { ConnectionEditor(connection: nil) { model.reload() } }
        .sheet(item: $editing) { ConnectionEditor(connection: $0) { model.reload() } }
        .confirmationDialog("Remove this connection and its cached files?", isPresented: Binding(
            get: { removing != nil }, set: { if !$0 { removing = nil } })) {
                Button("Remove Connection", role: .destructive) {
                    if let removing { Task { await model.remove(removing) } }
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
            .onAppear { name = connection?.name ?? ""; address = connection?.filesystemURL.absoluteString ?? "" }
        }.interactiveDismissDisabled(busy)
    }
    private func save() async {
        busy = true; defer { busy = false }
        do {
            let url = try FilesystemClient.validateURL(address)
            let credentialID = connection?.filesystemTokenID ?? connection?.id ?? UUID()
            let c = Connection(id: connection?.id ?? credentialID,
                               name: name.trimmingCharacters(in: .whitespaces),
                               url: connection?.url ?? url,
                               companionURL: url, companionTokenID: credentialID)
            if let connection, connection.filesystemURL != url, token.isEmpty {
                throw NSError(domain: "fileBeans", code: 1, userInfo: [NSLocalizedDescriptionKey: "Enter a token when changing the server URL."])
            }
            let credential = try token.isEmpty ? TokenStore.read(credentialID) : token.trimmingCharacters(in: .whitespacesAndNewlines)
            let store = try SharedStore()
            let repo = try Repository(connection: c, store: store, token: credential)
            _ = try await repo.client.scopes()
            guard let companion = repo.companion,
                  try await companion.capabilities().features?.contains("filesystemV1") == true else { throw APIError.incompatible }
            let previousToken = try? TokenStore.read(credentialID)
            try TokenStore.save(credential, for: credentialID)
            let domain = NSFileProviderDomain(identifier: .init(c.id.uuidString), displayName: c.name)
            do {
                try store.save(c)
                // Adding an existing domain updates its configuration.
                try await NSFileProviderManager.add(domain)
            } catch {
                if let previousToken { try? TokenStore.save(previousToken, for: credentialID) }
                else { try? TokenStore.remove(credentialID) }
                if let connection { try? store.save(connection) }
                else { try? store.remove(c.id) }
                throw error
            }
            await ThumbnailCache.shared.clear(connection: c.id)
            repo.notify()
            saved(); NotificationCenter.default.post(name: .fileBeansConnectionChanged, object: nil); dismiss()
        } catch { self.error = error.localizedDescription }
    }
}

extension Notification.Name { static let fileBeansConnectionChanged = Notification.Name("fileBeans.connectionChanged") }
