import SwiftUI

struct TransferList: View {
    let connection: Connection
    @State private var jobs: [CopyJob] = []
    @State private var error: String?
    @Environment(\.scenePhase) private var phase
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            if jobs.isEmpty { Text("No copy jobs") }
            ForEach(jobs) { job in
                VStack(alignment: .leading, spacing: 6) {
                    Text(job.current.isEmpty ? "Server copy" : job.current).lineLimit(2)
                    Text(job.state.capitalized).font(.caption)
                    if job.total > 0 {
                        ProgressView(value: min(1, Double(job.done) / Double(job.total)))
                        Text("\(ByteCountFormatter.string(fromByteCount: job.done, countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: job.total, countStyle: .file)) · \(ByteCountFormatter.string(fromByteCount: Int64(job.speed), countStyle: .file))/s").font(.caption)
                    }
                    if let detail = job.error, !detail.isEmpty { Text(detail).foregroundStyle(.red).font(.caption) }
                    if job.active { Button("Cancel", role: .destructive) { action("jobs/\(job.id)/cancel") } }
                    if job.state == "failed" { Button("Retry") { action("jobs/\(job.id)/retry") } }
                }
            }
        }
        .navigationTitle("Transfers")
        .refreshable { await refresh() }
        .task(id: phase) {
            guard phase == .active else { return }
            while !Task.isCancelled {
                await refresh()
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }
    private func refresh() async {
        let client = CompanionClient(connection: connection); defer { client.close() }
        do { jobs = try await client.jobs(); error = nil } catch { self.error = error.localizedDescription }
    }
    private func action(_ route: String) {
        Task {
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { _ = try await client.request(route, method: "POST"); await refresh() } catch { self.error = error.localizedDescription }
        }
    }
}

struct TrashList: View {
    let connection: Connection
    var source: String? = nil
    @State private var entries: [TrashEntry] = []
    @State private var error: String?
    @State private var permanent: TrashEntry?
    @State private var emptying = false
    @State private var busy = false
    private var visible: [TrashEntry] { entries.filter { source == nil || $0.source == source } }
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            if visible.isEmpty { Text("Trash is empty") }
            ForEach(visible) { entry in
                VStack(alignment: .leading, spacing: 5) {
                    Label(entry.name, systemImage: "trash")
                    Text("\(entry.source): /\(entry.original)").font(.caption)
                    Text("Expires \(Date(timeIntervalSince1970: Double(entry.expires)).formatted())").font(.caption)
                    if entry.state == "retained" {
                        HStack {
                            Button("Restore") { action("trash/\(entry.id)/restore") }
                            Button("Delete Permanently", role: .destructive) { permanent = entry }
                        }.buttonStyle(.borderless)
                    } else { Text(entry.error ?? entry.state).font(.caption) }
                }
            }
        }
        .disabled(busy)
        .navigationTitle("Trash")
        .toolbar { Button("Empty Trash", role: .destructive) { emptying = true }.disabled(visible.isEmpty || busy) }
        .task { await refresh() }
        .refreshable { await refresh() }
        .confirmationDialog("Permanently delete this item? This cannot be undone.", isPresented: Binding(get: { permanent != nil }, set: { if !$0 { permanent = nil } }), titleVisibility: .visible) {
            if let entry = permanent { Button("Delete Permanently", role: .destructive) { permanent = nil; action("trash/\(entry.id)/purge", permanent: true) } }
        }
        .confirmationDialog("Permanently delete all items shown in Trash? This cannot be undone.", isPresented: $emptying, titleVisibility: .visible) {
            Button("Empty Trash", role: .destructive) {
                Task {
                    busy = true; defer { busy = false }
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    do {
                        for entry in visible where entry.state == "retained" {
                            _ = try await client.request("trash/\(entry.id)/purge", method: "POST", permanent: true)
                        }
                        await refresh()
                    } catch { self.error = error.localizedDescription; await refreshKeepingError() }
                }
            }
        }
    }
    private func refresh() async {
        let client = CompanionClient(connection: connection); defer { client.close() }
        do { entries = try await client.trashEntries(); error = nil } catch { self.error = error.localizedDescription }
    }
    private func refreshKeepingError() async { let previous = error; await refresh(); if let previous { error = previous } }
    private func action(_ route: String, permanent: Bool = false) {
        Task {
            busy = true; defer { busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { _ = try await client.request(route, method: "POST", permanent: permanent); await refresh() }
            catch { self.error = error.localizedDescription; await refreshKeepingError() }
        }
    }
}

// These types are compiled only into the main app. No Drive item is StoredItem.
struct DriveFile: Decodable, Identifiable {
    let id: String
    let name: String
    let mimeType: String
    let size: String?
    var folder: Bool { mimeType == "application/vnd.google-apps.folder" }
    var supported: Bool { folder || !mimeType.hasPrefix("application/vnd.google-apps.") }
}
private struct SharedDrive: Decodable { let id: String; let name: String }
private struct SharedDrivePage: Decodable { let drives: [SharedDrive]; let nextPageToken: String? }
private struct DrivePage: Decodable { let files: [DriveFile]; let nextPageToken: String? }
struct DriveBrowser: View {
    let connection: Connection
    var folder = "root"
    var title = "Google Drive"
    @State private var files: [DriveFile] = []
    @State private var next = ""
    @State private var error: String?
    @State private var selected: DriveFile?
    @State private var busy = false
    @State private var connected = false
    @State private var disconnecting = false
    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var phase
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            if folder == "root" {
                NavigationLink("Shared with me") { DriveBrowser(connection: connection, folder: "shared", title: "Shared with me") }
                NavigationLink("Shared drives") { DriveBrowser(connection: connection, folder: "__drives", title: "Shared drives") }
            }
            ForEach(files) { file in
                Group {
                    if file.folder {
                        NavigationLink { DriveBrowser(connection: connection, folder: file.id, title: file.name) } label: { Label(file.name, systemImage: "folder") }
                    } else {
                        Label(file.name, systemImage: file.supported ? "doc" : "doc.badge.ellipsis")
                    }
                }
                .contextMenu { if file.supported && folder != "__drives" { Button("Copy to Server", systemImage: "doc.on.doc") { selected = file } } }
                if !file.folder && file.supported { Button("Copy \(file.name) to Server") { selected = file }.font(.caption) }
            }
            if !next.isEmpty { Button("Load More") { Task { await load(page: next) } } }
            if busy { ProgressView() }
        }
        .navigationTitle(title)
        .toolbar {
            if folder == "root" {
                Button(connected ? "Reconnect" : "Connect Drive") { connect() }
                if connected { Button("Disconnect", role: .destructive) { disconnecting = true } }
            }
        }
        .task(id: phase) { if phase == .active { await load() } }
        .refreshable { await load() }
        .confirmationDialog("Disconnect Google Drive? Unfinished Drive copies will wait for authorization.", isPresented: $disconnecting, titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) {
                Task {
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    do { _ = try await client.request("drive/disconnect", method: "POST"); files = []; connected = false }
                    catch { self.error = error.localizedDescription }
                }
            }
        }
        .sheet(item: $selected) { file in
            NavigationStack {
                DriveCopyDestination(connection: connection, file: file) { selected = nil }
            }
        }
    }
    private func load(page: String = "") async {
        guard !busy else { return }; busy = true; defer { busy = false }
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            connected = try await client.capabilities().driveConnected
            guard connected else { files = []; next = ""; return }
            if folder == "__drives" {
                let data = try await client.request("drive/drives", query: ["page": page])
                let result = try JSONDecoder().decode(SharedDrivePage.self, from: data)
                let items = result.drives.map { DriveFile(id: $0.id, name: $0.name, mimeType: "application/vnd.google-apps.folder", size: nil) }
                if page.isEmpty { files = items } else { files += items.filter { item in !files.contains { $0.id == item.id } } }
                next = result.nextPageToken ?? ""; error = nil; return
            }
            let data = try await client.request("drive/files", query: ["folder": folder, "page": page])
            let result = try JSONDecoder().decode(DrivePage.self, from: data)
            if page.isEmpty { files = result.files } else {
                let known = Set(files.map(\.id)); files += result.files.filter { !known.contains($0.id) }
            }
            next = result.nextPageToken ?? ""; error = nil
        } catch { self.error = error.localizedDescription }
    }
    private func connect() {
        Task {
            let client = CompanionClient(connection: connection); defer { client.close() }
            do {
                struct Authorization: Decodable { let url: URL }
                let auth = try JSONDecoder().decode(Authorization.self, from: await client.request("drive/connect", method: "POST"))
                guard auth.url.scheme == "https", auth.url.host == "accounts.google.com" else { throw APIError.invalidURL }
                openURL(auth.url)
            } catch { self.error = error.localizedDescription }
        }
    }
}
private struct DriveCopyDestination: View {
    let connection: Connection
    let file: DriveFile
    let dismiss: () -> Void
    @State private var repository: Repository?
    @State private var error: String?
    @State private var key = UUID().uuidString
    @State private var submitting = false
    var body: some View {
        Group {
            if let repository {
                FolderDestination(repository: repository, folder: nil, source: "", excluded: [], copying: true) { destination in
                    guard !submitting else { return }; submitting = true
                    Task {
                        defer { submitting = false }
                        let client = CompanionClient(connection: connection); defer { client.close() }
                        do {
                            _ = try await client.submit(BackgroundCopy(items: [CopyLocation(source: "drive", path: file.id)], destination: CopyLocation(source: destination.source, path: destination.file.path)), key: key)
                            dismiss()
                        } catch { self.error = error.localizedDescription }
                    }
                }.disabled(submitting)
            } else { ProgressView() }
        }
        .task { do { repository = try Repository(connection: connection, store: SharedStore()) } catch { self.error = error.localizedDescription } }
        .toolbar { Button("Cancel") { dismiss() } }
        .alert("Copy", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) { Button("OK") { error = nil } } message: { Text(error ?? "") }
    }
}
