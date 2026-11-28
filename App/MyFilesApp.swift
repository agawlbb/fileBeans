import SwiftUI
import FileProvider

@main
struct MyFilesApp: App {
    @UIApplicationDelegateAdaptor(TransferAppDelegate.self) private var delegate
    @Environment(\.scenePhase) private var phase
    var body: some Scene {
        WindowGroup {
            #if DEBUG
            if ProcessInfo.processInfo.arguments.contains("--folder-viewer-gesture-test") {
                FolderViewerFixture()
            } else { appContent }
            #else
            appContent
            #endif
        }
    }
    private var appContent: some View {
        SourcesView()
                .task { UploadQueue.shared.recover() }
                .onChange(of: phase) { _, phase in
                    if phase == .active { UploadQueue.shared.recover() }
                }
                .onReceive(NotificationCenter.default.publisher(for: UIApplication.didReceiveMemoryWarningNotification)) { _ in
                    Task { await ThumbnailCache.shared.clearMemory() }
                }
    }

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
            guard !UploadQueue.shared.records.contains(where: { $0.connection == connection.id }) else {
                throw NSError(domain: "fileBeans.Upload", code: 1, userInfo: [NSLocalizedDescriptionKey: "Cancel this connection’s uploads first."])
            }
            let domain = NSFileProviderDomain(identifier: .init(connection.id.uuidString), displayName: connection.name)
            if !connection.isGoogleDrive { try await NSFileProviderManager.remove(domain) }
            if connection.isGoogleDrive { try GoogleTokenStore.remove(connection.id) }
            else { try TokenStore.remove(connection.id) }
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
            VaultsView(connections: model.connections.filter { !$0.isGoogleDrive }, embedded: true)
            VaultKeyControls()
            Section("Public Hosting") {
                ForEach(model.connections.filter { !$0.isGoogleDrive }) { connection in
                    NavigationLink(connection.name) { HostingSettingsView(connection: connection) }
                }
            }
            Section("Google Drive") {
                ForEach(model.connections.filter { $0.isGoogleDrive }) { connection in
                    NavigationLink(connection.name) { DriveConnectionSettings(connection: connection) }
                }
            }
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
                Button("Add Source", systemImage: "plus") { adding = true }
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
    @State private var kind: SourceKind = .server
    @State private var name = ""
    @State private var address = ""
    @State private var token = ""
    @State private var localAddress = ""
    @State private var fingerprint = ""
    @State private var serverID = ""
    @State private var busy = false
    @State private var error: String?
    var body: some View {
        NavigationStack {
            Form {
                if connection == nil {
                    Picker("Source", selection: $kind) {
                        Text("FileBeans server").tag(SourceKind.server)
                        Text("Google Drive").tag(SourceKind.googleDrive)
                    }
                }
                TextField("Name", text: $name)
                if kind == .googleDrive {
                    Button(connection == nil ? "Sign in with Google" : "Reconnect Google Drive") {
                        Task { await connectGoogle() }
                    }.disabled(busy)
                } else {
                TextField("https://files.example.com", text: $address)
                    .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    .accessibilityLabel("Server URL")
                SecureField(connection == nil ? "API token" : "New API token (optional)", text: $token)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                Section("Local connection") {
                    TextField("https://192.168.12.246:8095", text: $localAddress)
                        .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .accessibilityLabel("Local server URL")
                    TextField("Server ID", text: $serverID).textInputAutocapitalization(.never).autocorrectionDisabled()
                    TextField("Certificate SHA-256", text: $fingerprint).textInputAutocapitalization(.never).autocorrectionDisabled()
                }
                }
                if let error { Text(error).foregroundStyle(.red) }
                if busy { ProgressView("Connecting…") }
            }
            .navigationTitle(connection == nil ? "Add Source" : "Edit Source")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(busy) }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(busy || name.trimmingCharacters(in: .whitespaces).isEmpty ||
                            (kind == .googleDrive ? connection == nil : address.isEmpty || (connection == nil && token.isEmpty)))
                }
            }
            .onAppear { kind = connection?.kind ?? .server; name = connection?.name ?? ""; address = connection?.filesystemURL.absoluteString ?? ""; localAddress = connection?.localURL?.absoluteString ?? ""; fingerprint = connection?.localFingerprint ?? ""; serverID = connection?.serverID ?? "" }
        }.interactiveDismissDisabled(busy)
    }
    private func connectGoogle() async {
        busy = true; defer { busy = false }
        do {
            var value = try await GoogleDriveSignIn.shared.connect(existing: connection)
            if !name.trimmingCharacters(in: .whitespaces).isEmpty { value.name = name.trimmingCharacters(in: .whitespaces) }
            try SharedStore().save(value)
            saved(); NotificationCenter.default.post(name: .fileBeansConnectionChanged, object: nil); dismiss()
        } catch { self.error = error.localizedDescription }
    }
    private func save() async {
        busy = true; defer { busy = false }
        do {
            if kind == .googleDrive {
                guard var value = connection else { throw APIError.authentication }
                value.name = name.trimmingCharacters(in: .whitespaces)
                try SharedStore().save(value)
                saved(); NotificationCenter.default.post(name: .fileBeansConnectionChanged, object: nil); dismiss()
                return
            }
            let url = try FilesystemClient.validateURL(address)
            let credentialID = connection?.filesystemTokenID ?? connection?.id ?? UUID()
            var c = Connection(id: connection?.id ?? credentialID,
                               name: name.trimmingCharacters(in: .whitespaces),
                               url: connection?.url ?? url,
                               companionURL: url, companionTokenID: credentialID)
            if !localAddress.isEmpty {
                c.localURL = try FilesystemClient.validateURL(localAddress)
                let pin = fingerprint.replacingOccurrences(of: ":", with: "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
                guard pin.count == 64, pin.allSatisfy({ "0123456789abcdef".contains($0) }), !serverID.isEmpty,
                      c.localURL?.host != url.host || c.localURL?.port != url.port else { throw APIError.invalidURL }
                c.localFingerprint = pin; c.serverID = serverID
            }
            if let connection, connection.filesystemURL != url, token.isEmpty {
                throw NSError(domain: "fileBeans", code: 1, userInfo: [NSLocalizedDescriptionKey: "Enter a token when changing the server URL."])
            }
            let credential = try token.isEmpty ? TokenStore.read(credentialID) : token.trimmingCharacters(in: .whitespacesAndNewlines)
            let store = try SharedStore()
            let repo = try Repository(connection: c, store: store, token: credential)
            _ = try await repo.client.scopes()
            guard let companion = repo.companion else { throw APIError.incompatible }
            let capabilities = try await companion.capabilities()
            guard capabilities.features?.contains("filesystemV1") == true else { throw APIError.incompatible }
            if let expected = c.serverID, capabilities.serverId != expected { throw APIError.incompatible }
            c.serverID = capabilities.serverId
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

final class TransferAppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String,
                     completionHandler: @escaping () -> Void) {
        UploadQueue.shared.handleEvents(identifier: identifier, completion: completionHandler)
    }
}
