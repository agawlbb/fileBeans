import SwiftUI

struct DriveTrashView: View {
    let connection: Connection
    @State private var scope = "root"
    @State private var drives: [SharedDrive] = []
    @State private var files: [DriveFile] = []
    @State private var next = ""
    @State private var error: String?
    @State private var busy = false
    @State private var pending: Set<String> = []
    @State private var writable = false
    @State private var permanent: DriveFile?
    @State private var emptying = false
    private var scopeName: String { drives.first { $0.id == scope }?.name ?? "My Drive" }
    var body: some View {
        List {
            Picker("Drive", selection: $scope) { Text("My Drive").tag("root"); ForEach(drives) { Text($0.name).tag($0.id) } }
            if let error { Text(error).foregroundStyle(.red) }
            if files.isEmpty && !busy { Text("Trash is empty") }
            ForEach(files) { file in
                VStack(alignment: .leading, spacing: 6) {
                    Label(file.name, systemImage: file.folder ? "folder.fill" : "doc")
                    HStack {
                        if writable && file.capabilities?.canUntrash == true { Button("Restore") { action(DriveMutationRequest(action: "restore", fileId: file.id, version: file.version)) } }
                        if writable && file.capabilities?.canDelete == true { Button("Delete Permanently", role: .destructive) { permanent = file } }
                    }.buttonStyle(.borderless)
                }
                .opacity(pending.contains(file.id) ? 0.45 : 1)
                .overlay(alignment: .trailing) { if pending.contains(file.id) { ProgressView() } }
            }
            if !next.isEmpty { Button("Load More") { Task { await load(page: next) } } }
        }
        .navigationTitle("Trash")
        .disabled(busy)
        .toolbar { Button("Empty Trash", role: .destructive) { emptying = true }.disabled(!writable || files.isEmpty || busy) }
        .task {
            let client = DriveClient(connection: connection); defer { client.close() }
            do {
                var page = ""
                repeat { let result = try await client.drives(page: page); drives += result.drives; page = result.nextPageToken ?? "" } while !page.isEmpty
            } catch { self.error = error.localizedDescription }
        }
        .task(id: scope) { await load() }
        .refreshable { await load() }
        .confirmationDialog("Delete permanently?", isPresented: Binding(get: { permanent != nil }, set: { if !$0 { permanent = nil } }), titleVisibility: .visible) {
            if let file = permanent { Button("Delete Permanently", role: .destructive) { permanent = nil; action(DriveMutationRequest(action: "purge", fileId: file.id, version: file.version), permanent: true) } }
        }
        .confirmationDialog("Empty \(scopeName) Trash?", isPresented: $emptying, titleVisibility: .visible) {
            Button("Empty Trash", role: .destructive) { action(DriveMutationRequest(action: "empty", scope: scope), permanent: true) }
        }
    }
    private func load(page: String = "") async {
        busy = true; defer { busy = false }
        let client = DriveClient(connection: connection); defer { client.close() }
        do {
            writable = try await client.transport.capabilities().driveWriteAccess == true
            let result = try await client.trash(scope: scope, page: page)
            if page.isEmpty { files = result.files } else { let known = Set(files.map(\.id)); files += result.files.filter { !known.contains($0.id) } }
            next = result.nextPageToken ?? ""; error = nil
        } catch { self.error = error.localizedDescription }
    }
    private func action(_ request: DriveMutationRequest, permanent: Bool = false) {
        pending = request.action == "empty" ? Set(files.map(\.id)) : Set([request.fileId].compactMap { $0 })
        Task {
            busy = true
            let client = DriveClient(connection: connection); defer { client.close(); busy = false; pending = [] }
            do { try await client.mutate(request, permanent: permanent); await load() }
            catch { self.error = error.localizedDescription }
        }
    }
}
