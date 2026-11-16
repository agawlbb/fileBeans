import SwiftUI

struct FolderDestination: View {
    let repository: Repository
    let folder: StoredItem?
    let source: String
    let excluded: [StoredItem]
    var copying = false
    let choose: (StoredItem) -> Void
    @State private var folders: [StoredItem] = []
    @State private var error: String?
    @State private var loading = true
    var body: some View {
        List(folders) { child in
            NavigationLink {
                FolderDestination(repository: repository, folder: child, source: source, excluded: excluded, copying: copying, choose: choose)
            } label: { Label(child.file.name, systemImage: "folder") }
        }
        .navigationTitle(folder?.file.name ?? (copying ? "Copy To" : "Move To"))
        .safeAreaInset(edge: .bottom) {
            if let folder {
                Button(copying ? "Copy Here" : "Move Here") { choose(folder) }
                    .buttonStyle(.borderedProminent).padding().disabled(!folder.permissions.create || (copying && excluded.contains { $0.parent == folder.id }))
            }
        }
        .overlay {
            if loading { ProgressView() }
            else if let error { ContentUnavailableView(error, systemImage: "exclamationmark.triangle") }
        }
        .task {
            defer { loading = false }
            do {
                folders = try await repository.children(folder).filter { child in
                    (copying || child.source == source) && child.file.isDirectory &&
                    !excluded.contains { $0.source == child.source && $0.file.isDirectory && FilePath.contains(child.file.path, in: $0.file.path) }
                }
            } catch { self.error = error.localizedDescription }
        }
    }
}
