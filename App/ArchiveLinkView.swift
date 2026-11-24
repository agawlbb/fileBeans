import SwiftUI

struct ArchiveLinkView: View {
    let connection: Connection
    let folder: StoredItem
    let completed: () -> Void
    @State private var link: ArchiveLink?
    @State private var loaded = false
    @State private var picking = false
    @State private var confirmingUnlink = false
    @State private var busy = true
    @State private var error: String?
    @State private var destination: StoredItem?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Form {
                if let link {
                    endpoint(link.library, title: "Library")
                    endpoint(link.archive, title: "Archive")
                    Button("Unlink", role: .destructive) { confirmingUnlink = true }
                } else if !busy {
                    Section("Library") { Text(folder.file.name) }
                    Button("Choose Archive Folder…") { picking = true }
                }
                if let error { Text(error).foregroundStyle(.red) }
            }
            .navigationTitle(link == nil ? "Link Archive" : "Linked Archive")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { dismiss() }.disabled(busy) } }
            .disabled(busy)
            .overlay { if busy { ProgressView() } }
            .navigationDestination(isPresented: $picking) {
                SourceDestinationPicker(connection: connection, mode: .archive, excludedLocal: [folder], excludedDrive: [],
                    protectedFolderSheet: { parent in AnyView(NewProtectedFolder(connection: connection, parent: parent) {}) }) { target in
                    guard case .local(let archive) = target else { return }
                    picking = false
                    perform { client in
                        link = try await client.linkArchive(library: folder, archive: archive)
                        completed()
                    }
                }
            }
            .navigationDestination(for: SourceDestination.self) { $0.destination }
            .navigationDestination(item: $destination) { item in BrowserView(connection: connection, parent: item) }
            .confirmationDialog("Unlink Archive?", isPresented: $confirmingUnlink, titleVisibility: .visible) {
                Button("Unlink", role: .destructive) {
                    guard let link else { return }
                    perform { client in try await client.unlinkArchive(link.id); completed(); dismiss() }
                }
            }
            .task {
                guard !loaded else { return }
                loaded = true
                perform { client in
                    let links = try await client.archiveLinks()
                    let location = try client.sourceLocation(CopyLocation(source: folder.source, path: folder.file.path))
                    link = links.first { value in
                        value.id == folder.file.archiveLink || [value.library, value.archive].contains {
                            $0.source == location.source && $0.path == location.path
                        }
                    }
                }
            }
        }
        .environment(\.folderBreadcrumbs, FolderBreadcrumbContext())
    }
    private func endpoint(_ value: ArchiveEndpoint, title: String) -> some View {
        Section(title) {
            Button {
                perform { _ in
                    let repository = try Repository(connection: connection, store: SharedStore())
                    guard var current = try await repository.roots().first(where: { $0.source == value.source }) else { throw APIError.missing }
                    let scope = try repository.store.sourceScope(connection.id, source: value.source)
                    guard FilePath.contains(value.path, in: scope) else { throw APIError.forbidden }
                    let relative = String(value.path.dropFirst(scope == "/" ? 1 : scope.count)).split(separator: "/")
                    for part in relative {
                        guard let child = try await repository.children(current).first(where: { $0.file.name == String(part) && $0.file.isDirectory }) else { throw APIError.missing }
                        current = child
                    }
                    destination = current
                }
            } label: {
                LabeledContent(value.source, value: value.path)
            }.disabled(!value.available)
            if !value.available { Text("Unavailable").foregroundStyle(.secondary) }
        }
    }
    private func perform(_ action: @escaping @MainActor (CompanionClient) async throws -> Void) {
        busy = true; error = nil
        Task { @MainActor in
            defer { busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { try await action(client) } catch { self.error = error.localizedDescription }
        }
    }
}
