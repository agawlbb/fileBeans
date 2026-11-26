import SwiftUI

struct TransferList: View {
    let connection: Connection
    @ObservedObject private var uploads = UploadActivity.shared
    @ObservedObject private var activity = FileActivity.shared
    @State private var showHistory = false
    @State private var jobs: [CopyJob] = []
    @State private var error: String?
    @State private var archives: ArchiveSyncStatus?
    @State private var archiveError: String?
    @State private var supportsArchives: Bool?
    @State private var refreshing = false
    @State private var pendingActions: Set<String> = []
    @Environment(\.scenePhase) private var phase
    private var visibleJobs: [CopyJob] { activity.visibleTransfers(jobs, connection: connection.id, history: showHistory) }
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            let activeUploads = uploads.transfers(connection: connection.id)
            if visibleJobs.isEmpty && activeUploads.isEmpty && archives?.hasPending != true && error == nil && archiveError == nil { Text("No transfers") }
            ForEach(activeUploads.filter { upload in !visibleJobs.contains { $0.id == upload.job } }) { upload in
                VStack(alignment: .leading, spacing: 6) {
                    Text(upload.name).lineLimit(2)
                    Text("To \(upload.destination)").font(.caption).lineLimit(2)
                    Text(upload.state).font(.caption)
                    ProgressView(value: upload.progress)
                    Text("\(ByteCountFormatter.string(fromByteCount: Int64(Double(upload.total) * upload.progress), countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: upload.total, countStyle: .file))").font(.caption)
                    if let error = upload.error {
                        Text(error).foregroundStyle(.red).font(.caption)
                        Button("Retry") { UploadQueue.shared.retry(upload.id) }
                    }
                    Button("Cancel", role: .destructive) { UploadQueue.shared.cancel(upload.id) }
                }
            }
            ForEach(visibleJobs) { job in
                VStack(alignment: .leading, spacing: 6) {
                    Text(job.title).lineLimit(2)
                    if let request = job.request {
                        Text("From \(request.items.map(\.displayLocation).joined(separator: ", "))").font(.caption).lineLimit(2)
                        Text("To \(request.destination.displayLocation)").font(.caption).lineLimit(2)
                    }
                    if !job.current.isEmpty { Text(job.current.replacingOccurrences(of: "publish:", with: "Finishing ")).font(.caption).lineLimit(2) }
                    Text(job.active && job.cancel == true ? "Cancelling…" : job.state.capitalized).font(.caption)
                    if job.progressUnit == "items", let total = job.itemsTotal, total > 0 {
                        ProgressView(value: Double(job.itemsDone ?? 0) / Double(total))
                        Text("\(job.itemsDone ?? 0) / \(total) items").font(.caption)
                    } else if job.total > 0 {
                        ProgressView(value: min(1, Double(job.done) / Double(job.total)))
                        Text("\(ByteCountFormatter.string(fromByteCount: job.done, countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: job.total, countStyle: .file)) · \(ByteCountFormatter.string(fromByteCount: Int64(job.speed), countStyle: .file))/s").font(.caption)
                    }
                    if let detail = job.error, !detail.isEmpty { Text(detail).foregroundStyle(.red).font(.caption) }
                    if job.active {
                        Button(pendingActions.contains(job.id) || job.cancel == true ? "Cancelling…" : "Cancel", role: .destructive) { action("jobs/\(job.id)/cancel", id: job.id) }
                            .disabled(pendingActions.contains(job.id) || job.cancel == true || job.canCancel == false)
                    }
                    if job.state == "failed" { Button("Retry") { action("jobs/\(job.id)/retry", id: job.id) }.disabled(pendingActions.contains(job.id)) }
                    if job.state == "failed" && !showHistory {
                        Button("Dismiss") { activity.dismissFailure(job.id, connection: connection.id) }
                    }
                }
                .buttonStyle(.borderless)
            }
            Toggle("Show history", isOn: $showHistory)
            archiveSection
        }
        .navigationTitle("Transfers")
        .refreshable { await refresh() }
        .task(id: phase) {
            guard phase == .active else { return }
            DriveUploadQueue.shared(connection: connection).recover()
            while !Task.isCancelled {
                await refresh()
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }
    private func refresh() async {
        guard !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        let client = CompanionClient(connection: connection); defer { client.close() }
        client.prepareForJobRetry = { try await ServerRequestRecovery.waitUntilActive() }
        async let transfers: Void = refreshJobs(client)
        async let archiveWork: Void = refreshArchives(client)
        _ = await (transfers, archiveWork)
    }
    private func refreshJobs(_ client: CompanionClient) async {
        do {
            let incoming = try await client.jobs()
            guard !Task.isCancelled, phase == .active else { return }
            activity.synchronize(incoming, connection: connection.id)
            jobs = incoming
            error = nil
        } catch {
            guard !Task.isCancelled, phase == .active else { return }
            self.error = error.localizedDescription
        }
    }
    private func refreshArchives(_ client: CompanionClient) async {
        do {
            let status = try await loadArchives(client)
            guard !Task.isCancelled, phase == .active else { return }
            archives = status
            archiveError = nil
        } catch {
            guard !Task.isCancelled, phase == .active else { return }
            archiveError = error.localizedDescription
        }
    }
    private func loadArchives(_ client: CompanionClient) async throws -> ArchiveSyncStatus? {
        if supportsArchives == nil {
            supportsArchives = try await client.capabilities().features?.contains("archiveSyncV1") == true
        }
        guard supportsArchives == true else { return nil }
        return try await client.archiveSyncStatus()
    }
    @ViewBuilder private var archiveSection: some View {
        if supportsArchives != false {
            Section("Archive") {
                if let archiveError { Text(archiveError).foregroundStyle(.red) }
                else if let archives {
                    if let items = archives.items, !items.isEmpty {
                        ForEach(items) { item in
                            VStack(alignment: .leading, spacing: 6) {
                                HStack {
                                    Text(item.name).lineLimit(2)
                                    Spacer()
                                    if item.active { ProgressView() }
                                }
                                Text("\(item.source) → \(item.destination)").font(.caption)
                                Text(item.status).font(.caption)
                                if let detail = item.error, !detail.isEmpty {
                                    Text(detail).font(.caption).foregroundStyle(.red)
                                    if let next = item.next, next > Int64(Date().timeIntervalSince1970) {
                                        Text("Retry \(Date(timeIntervalSince1970: Double(next)).formatted(date: .omitted, time: .shortened))").font(.caption)
                                    }
                                }
                            }
                        }
                    } else if archives.hasPending {
                        if let pending = archives.pendingWork { LabeledContent("Pending", value: String(pending)) }
                        if archives.pendingOriginals > 0 { LabeledContent("Awaiting copy", value: String(archives.pendingOriginals)) }
                        if archives.pendingChanges > 0 { LabeledContent("Changes", value: String(archives.pendingChanges)) }
                        if archives.problems > 0 { LabeledContent("Needs attention", value: String(archives.problems)) }
                    } else { Text("Up to date") }
                } else if archiveError == nil { ProgressView() }
            }
        }
    }
    private func action(_ route: String, id: String) {
        pendingActions.insert(id)
        Task {
            defer { pendingActions.remove(id) }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { _ = try await client.request(route, method: "POST"); await refresh() } catch { self.error = error.localizedDescription }
        }
    }
}

struct TrashList: View {
    let connection: Connection
    let source: String
    @State private var entries: [TrashEntry] = []
    @State private var loaded = false
    @State private var error: String?
    @State private var permanent: TrashEntry?
    @State private var emptying = false
    @State private var busy = false
    @State private var pending: Set<String> = []
    private var visible: [TrashEntry] { entries.filter { $0.source == source && ($0.state == "retained" || $0.state == "purging") } }
    var body: some View {
        List {
            if let error { Text(error).foregroundStyle(.red) }
            if !loaded && error == nil { ProgressView() }
            else if visible.isEmpty && error == nil { Text("Trash is empty") }
            ForEach(visible) { entry in
                VStack(alignment: .leading, spacing: 5) {
                    Label(entry.name, systemImage: "trash")
                    Text((entry.original as NSString).deletingLastPathComponent).font(.caption)
                    Text("Expires \(Date(timeIntervalSince1970: Double(entry.expires)).formatted())").font(.caption)
                    if entry.state == "retained" {
                        HStack {
                            Button("Restore") { action("trash/\(entry.id)/restore") }
                            Button("Delete Permanently", role: .destructive) { permanent = entry }
                        }.buttonStyle(.borderless)
                    } else { Text(entry.error ?? entry.state).font(.caption) }
                }
                .opacity(pending.contains(entry.id) ? 0.45 : 1)
                .overlay(alignment: .trailing) { if pending.contains(entry.id) { ProgressView() } }
            }
        }
        .disabled(busy)
        .navigationTitle("Trash")
        .toolbar { Button("Empty Trash", role: .destructive) { emptying = true }.disabled(visible.isEmpty || busy) }
        .task { while !Task.isCancelled { await refreshKeepingError(); do { try await Task.sleep(for: .seconds(3)) } catch { return } } }
        .refreshable { await refresh() }
        .confirmationDialog("Delete permanently?", isPresented: Binding(get: { permanent != nil }, set: { if !$0 { permanent = nil } }), titleVisibility: .visible) {
            if let entry = permanent { Button("Delete Permanently", role: .destructive) { permanent = nil; action("trash/\(entry.id)/purge", permanent: true) } }
        }
        .confirmationDialog("Empty Trash?", isPresented: $emptying, titleVisibility: .visible) {
            Button("Empty Trash", role: .destructive) {
                Task {
                    busy = true; pending = Set(visible.map(\.id)); defer { busy = false; pending = [] }
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
        do { entries = try await client.trashEntries(source: source); loaded = true; error = nil } catch { self.error = error.localizedDescription }
    }
    private func refreshKeepingError() async { let previous = error; await refresh(); if let previous { error = previous } }
    private func action(_ route: String, permanent: Bool = false) {
        let id = route.split(separator: "/").dropFirst().first.map(String.init)
        if let id { pending.insert(id) }
        Task {
            busy = true; defer { busy = false; if let id { pending.remove(id) } }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { _ = try await client.request(route, method: "POST", permanent: permanent); await refresh() }
            catch { self.error = error.localizedDescription; await refreshKeepingError() }
        }
    }
}
