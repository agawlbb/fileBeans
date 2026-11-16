import SwiftUI
import CryptoKit

struct NormalizationStatus: Decodable {
    let enabled: Bool
    let source: String
    let path: String
    let profile: String
    let driveFolder: String?
    let sequential: Bool
}
struct NormalizationJob: Decodable, Identifiable {
    struct Request: Codable { let source: String; let path: String; var relativePath: String? = nil }
    let id: String
    let request: Request
    let state: String
    let phase: String
    let sourceSize: Int64
    let outputSize: Int64
    let outputPath: String?
    let done: Int64
    let total: Int64
    let error: String?
    var active: Bool { !["completed", "failed", "cancelled"].contains(state) }
    var title: String { outputPath ?? request.relativePath ?? request.path }
    var phaseTitle: String {
        if state == "waiting" { return "Waiting to retry" }
        switch phase {
        case "inspecting": return "Inspecting media"
        case "downloading": return "Downloading from Drive"
        case "converting": return "Optimizing media"
        case "validating": return "Checking output"
        case "publishing": return "Finishing"
        default: return state.capitalized
        }
    }
}
struct NormalizationBatch: Decodable, Identifiable {
    let id: String
    let state: String
    let total: Int
    let completed: Int
    let failed: Int
    let error: String?
}
final class NormalizationClient {
    let transport: CompanionClient
    init(connection: Connection) { transport = CompanionClient(connection: connection) }
    init(transport: CompanionClient) { self.transport = transport }
    func close() { transport.close() }
    func submit(_ request: NormalizationJob.Request) async throws {
        let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys
        let body = try encoder.encode(request)
        let identity = Data((transport.connection.id.uuidString + (transport.connection.companionURL?.absoluteString ?? "")).utf8) + body
        let name = SHA256.hash(data: identity).map { String(format: "%02x", $0) }.joined()
        let directory = try AppConfiguration.directory.appendingPathComponent("normalization-submissions", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let pending = directory.appendingPathComponent(name + ".key")
        let key = (try? String(contentsOf: pending, encoding: .utf8)) ?? UUID().uuidString
        try Data(key.utf8).write(to: pending, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        do {
            _ = try await transport.request("normalization/jobs", method: "POST", body: body, key: key)
            try? FileManager.default.removeItem(at: pending)
        } catch {
            let failure = error as NSError
            if failure.domain == "fileBeans.Companion", [400, 403, 404, 409].contains(failure.code) { try? FileManager.default.removeItem(at: pending) }
            throw error
        }
    }
}
struct NormalizationView: View {
    let connection: Connection
    @State private var jobs: [NormalizationJob] = []
    @State private var batches: [NormalizationBatch] = []
    @State private var status: NormalizationStatus?
    @State private var supported = true
    @State private var error: String?
    @State private var importing = false
    @State private var busy = false
    @State private var showCompleted = false
    @Environment(\.scenePhase) private var scenePhase
    private var visible: [NormalizationJob] { jobs.filter { showCompleted || !["completed", "cancelled"].contains($0.state) } }
    var body: some View {
        List {
            if !supported { Text("Update the companion service to enable media normalization.") }
            if let status {
                Section {
                    Label(status.source + status.path, systemImage: "folder")
                    Text(status.enabled ? "One file downloads and finishes processing before the next begins." : "Media normalization is not enabled on this server.")
                        .font(.caption).foregroundStyle(.secondary)
                    if status.enabled, let folder = status.driveFolder, !folder.isEmpty {
                        Button("Import Google Drive pics") { importing = true }.disabled(busy)
                    }
                }
            }
            if let error { Text(error).foregroundStyle(.red) }
            ForEach(batches) { batch in
                Section("Drive import") {
                    Text("\(batch.completed) of \(batch.total) files completed")
                    if batch.total > 0 { ProgressView(value: Double(batch.completed + batch.failed) / Double(batch.total)) }
                    if batch.failed > 0 { Text("\(batch.failed) item(s) need attention").font(.caption).foregroundStyle(.secondary) }
                    if let detail = batch.error, !detail.isEmpty { Text(detail).font(.caption).foregroundStyle(.red) }
                    if ["queued", "running"].contains(batch.state) {
                        Button("Cancel Import", role: .destructive) { action("batches/" + batch.id + "/cancel") }.disabled(busy)
                    }
                }
            }
            Section("Files") {
                Toggle("Show completed", isOn: $showCompleted)
                if visible.isEmpty { Text("No pending normalization jobs").foregroundStyle(.secondary) }
                ForEach(visible) { job in NormalizationJobRow(job: job, busy: busy, action: action) }
            }
        }
        .navigationTitle("Media Normalization")
        .refreshable { await refresh() }
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                await refresh()
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
        .confirmationDialog("Import Drive pics into the VPS media folder? Files are processed one at a time. Drive originals stay unchanged.", isPresented: $importing, titleVisibility: .visible) {
            Button("Start Import") {
                guard let folder = status?.driveFolder else { return }
                Task {
                    busy = true; defer { busy = false }
                    let client = NormalizationClient(connection: connection); defer { client.close() }
                    do { try await client.submit(.init(source: "drive", path: folder)); await refresh() }
                    catch { self.error = error.localizedDescription }
                }
            }
        }
    }
    private func refresh() async {
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            supported = try await client.capabilities().features?.contains("normalization") == true
            guard supported else { return }
            status = try JSONDecoder().decode(NormalizationStatus.self, from: await client.request("normalization/status"))
            jobs = try JSONDecoder().decode([NormalizationJob].self, from: await client.request("normalization/jobs"))
            batches = try JSONDecoder().decode([NormalizationBatch].self, from: await client.request("normalization/batches"))
            error = nil
        } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
    }
    private func action(_ route: String) {
        Task {
            busy = true; defer { busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do { _ = try await client.request("normalization/" + route, method: "POST"); await refresh() }
            catch { self.error = error.localizedDescription }
        }
    }
}
private struct NormalizationJobRow: View {
    let job: NormalizationJob
    let busy: Bool
    let action: (String) -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(job.title).lineLimit(2)
            Text(job.phaseTitle).font(.caption).foregroundStyle(.secondary)
            if job.total > 0, job.active {
                ProgressView(value: min(1, Double(job.done) / Double(job.total)))
                if job.phase == "downloading" {
                    Text("\(ByteCountFormatter.string(fromByteCount: job.done, countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: job.total, countStyle: .file))").font(.caption)
                } else { Text("\(Int(min(100, Double(job.done) / Double(job.total) * 100)))%").font(.caption) }
            }
            if job.state == "completed" {
                Text("\(ByteCountFormatter.string(fromByteCount: job.sourceSize, countStyle: .file)) → \(ByteCountFormatter.string(fromByteCount: job.outputSize, countStyle: .file))").font(.caption)
            }
            if let detail = job.error, !detail.isEmpty {
                Text(detail).font(.caption).foregroundStyle(.red)
                Text("Job \(job.id)").font(.caption2).textSelection(.enabled)
            }
            if job.active { Button("Cancel", role: .destructive) { action("jobs/" + job.id + "/cancel") }.disabled(busy) }
            if ["failed", "cancelled"].contains(job.state) { Button("Retry") { action("jobs/" + job.id + "/retry") }.disabled(busy) }
        }
    }
}
