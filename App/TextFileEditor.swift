import SwiftUI

struct TextFileEditor: View {
    let repository: Repository
    let parent: StoredItem
    let item: StoredItem?
    let saved: () -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var text = ""
    @State private var original = ""
    @State private var format: EditableText?
    @State private var revision: String?
    @State private var ready = false
    @State private var busy = false
    @State private var error: String?
    @State private var discarding = false

    private var changed: Bool { item == nil ? !name.isEmpty || !text.isEmpty : text != original }

    var body: some View {
        NavigationStack {
            VStack {
                if item == nil {
                    TextField("Name", text: $name)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .padding(.horizontal)
                }
                if ready {
                    TextEditor(text: $text).font(.body.monospaced())
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                } else if busy { ProgressView() }
                else { Button("Retry") { Task { await load() } } }
            }
            .navigationTitle(item?.file.name ?? "New Text File")
            .navigationBarTitleDisplayMode(.inline)
            .disabled(busy)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { if changed { discarding = true } else { dismiss() } }.disabled(busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(busy || !ready || (item == nil ? name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty : !changed))
                }
            }
            .task { await load() }
            .interactiveDismissDisabled(changed || busy)
            .confirmationDialog("Discard Changes?", isPresented: $discarding, titleVisibility: .visible) {
                Button("Discard", role: .destructive) { dismiss() }
            }
            .alert("Text File", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
                Button("OK") { error = nil }
            } message: { Text(error ?? "") }
        }
    }

    @MainActor private func load() async {
        guard !ready else { return }
        guard let item else { ready = true; return }
        busy = true
        defer { busy = false }
        do {
            try repository.requireUnlocked(item)
            guard item.permissions.download, item.permissions.modify else { throw APIError.forbidden }
            let resource = try await repository.client.resource(source: item.source, path: item.file.path)
            if let size = resource.size, size > Int64(EditableText.maximumBytes) { throw CocoaError(.fileReadTooLarge) }
            guard let version = resource.revision, !version.isEmpty else { throw APIError.conflict }
            var request = try repository.client.request("content", query: ["source": item.source, "path": item.file.path])
            request.setValue("\"" + version + "\"", forHTTPHeaderField: "If-Match")
            let bytes = try await repository.client.data(request, limit: EditableText.maximumBytes)
            let latest = try await repository.client.resource(source: item.source, path: item.file.path)
            guard latest.revision == version else { throw APIError.conflict }
            let decoded = try EditableText(data: bytes)
            text = decoded.text; original = decoded.text; format = decoded; revision = version; ready = true
        } catch { self.error = error.localizedDescription }
    }

    @MainActor private func save() async {
        busy = true
        defer { busy = false }
        do {
            try await repository.refreshProtection()
            try repository.requireUnlocked(parent)
            if let item { try repository.requireUnlocked(item) }
            guard (item?.permissions.modify ?? parent.permissions.create) else { throw APIError.forbidden }
            let filename = item?.file.name ?? EditableText.filename(name)
            let path = try FilePath.child(filename, in: parent.file.path)
            guard !CompanionClient.protected(path) else { throw APIError.forbidden }
            let temporary = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            defer { try? FileManager.default.removeItem(at: temporary) }
            let codec = try format ?? EditableText(data: Data())
            try codec.data(for: text).write(to: temporary, options: .atomic)
            try await repository.client.upload(source: parent.source, path: path, file: temporary,
                                               replacing: item != nil, expectedRevision: revision)
            repository.notify(parent.id)
            saved(); dismiss()
        } catch { self.error = error.localizedDescription }
    }
}
