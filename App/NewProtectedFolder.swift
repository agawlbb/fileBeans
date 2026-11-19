import SwiftUI

@MainActor
final class ProtectedFolderDraft: ObservableObject {
    static let shared = ProtectedFolderDraft()
    private let saved = "vault.creationDraft.v1"
    @Published var name = "" { didSet { if !loading && name != oldValue { requestID = UUID().uuidString }; persist() } }
    private(set) var requestID = UUID().uuidString
    private var location = ""
    private var loading = false
    private let defaults: UserDefaults
    init(defaults: UserDefaults = .standard) { self.defaults = defaults }
    func begin(connection: UUID, parent: StoredItem) {
        loading = true; defer { loading = false }
        let target = connection.uuidString + ":" + parent.source + ":" + parent.file.path
        if let data = defaults.dictionary(forKey: saved), data["location"] as? String == target {
            location = target; requestID = data["requestID"] as? String ?? UUID().uuidString
            name = data["name"] as? String ?? ""
        } else { location = target; requestID = UUID().uuidString; name = "" }
    }
    func finish() { location = ""; name = ""; defaults.removeObject(forKey: saved) }
    private func persist() {
        guard !location.isEmpty else { return }
        defaults.set(["location": location, "name": name, "requestID": requestID], forKey: saved)
    }
}

struct NewProtectedFolder: View {
    let connection: Connection
    let parent: StoredItem
    let completed: () -> Void
    @ObservedObject private var draft = ProtectedFolderDraft.shared
    @ObservedObject private var keys = VaultKeySession.shared
    @State private var busy = false
    @State private var error: String?
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack {
            Form {
                Section { TextField("Name", text: $draft.name).autocorrectionDisabled() }
                if !keys.backupConfirmed {
                    VaultKeyControls(session: keys)
                }
            }
            .navigationTitle("Protected Folder")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(busy) }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") { create() }.disabled(busy || draft.name.isEmpty || !keys.backupConfirmed)
                }
            }
            .disabled(busy)
            .overlay { if busy { ProgressView() } }
            .overlay { if scenePhase != .active { Color(.systemBackground) } }
            .task { draft.begin(connection: connection.id, parent: parent) }
            .alert("Protected Folder", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
                Button("OK") { error = nil }
            } message: { Text(error ?? "") }
        }
        .interactiveDismissDisabled(busy)
        .onDisappear { draft.finish(); keys.hide() }
    }
    private func create() {
        busy = true
        Task { @MainActor in
            defer { busy = false }
            do {
                _ = try FilePath.child(draft.name, in: parent.file.path)
                let filesystem = FilesystemClient(connection: connection); defer { filesystem.close() }
                guard let scope = try await filesystem.scopes().first(where: { $0.name == parent.source }),
                      scope.permissions.create else { throw APIError.forbidden }
                let sourceParent = try FilePath.inSource(parent.file.path, scope: scope.scope)
                let code = try await keys.authorizedCode()
                let client = CompanionClient(connection: connection); defer { client.close() }
                _ = try await client.createProtectedFolder(source: parent.source, parent: sourceParent,
                    name: draft.name, code: code, requestID: draft.requestID)
                draft.finish(); keys.hide(); completed(); dismiss()
            } catch { self.error = error.localizedDescription }
        }
    }
}
