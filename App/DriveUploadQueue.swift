import SwiftUI

@MainActor
final class DriveUploadQueue: ObservableObject {
    private static var queues: [UUID: DriveUploadQueue] = [:]
    static func shared(connection: Connection) -> DriveUploadQueue {
        if let existing = queues[connection.id], existing.connection == connection { return existing }
        let queue = DriveUploadQueue(connection: connection); queues[connection.id] = queue; return queue
    }
    struct Record: Codable {
        let id: UUID
        let name: String
        let parent: String
        let size: Int64
        let created: Date
        let key: String
        var session: String?
        var job: String?
        var cancelRequested: Bool?
    }
    let connection: Connection
    @Published var name = ""
    @Published var phase = ""
    @Published var progress: Double = 0
    @Published var busy = false
    @Published var error: String?
    @Published var canRetry = false
    private var task: Task<Void, Never>?
    private var explicitCancel = false
    private var pending: [Record] = []
    private var directory: URL { get throws { try AppConfiguration.directory.appendingPathComponent("drive-uploads/" + connection.id.uuidString, isDirectory: true) } }
    private init(connection: Connection) { self.connection = connection }
    func recover() {
        guard task == nil else { return }
        do {
            let directory = try directory
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            pending = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
                .filter { $0.pathExtension == "json" }
                .map { try JSONDecoder().decode(Record.self, from: Data(contentsOf: $0)) }.sorted { $0.created < $1.created }
            publishUploads()
            start()
        } catch { self.error = error.localizedDescription }
    }
    func enqueue(_ urls: [URL], parent: String, scoped: Bool = true) {
        do {
            let directory = try directory
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            for url in urls {
                _ = try FilePath.child(url.lastPathComponent, in: "/")
                let access = scoped && url.startAccessingSecurityScopedResource()
                defer { if access { url.stopAccessingSecurityScopedResource() } }
                let id = UUID()
                try FileManager.default.copyItem(at: url, to: directory.appendingPathComponent(id.uuidString + ".data"))
                let record = Record(id: id, name: url.lastPathComponent, parent: parent,
                                    size: (try url.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? 0,
                                    created: Date(), key: UUID().uuidString)
                try save(record); pending.append(record)
            }
            publishUploads()
            start()
        } catch { self.error = error.localizedDescription; canRetry = true }
    }
    private func publishUploads() {
        for record in pending {
            UploadActivity.shared.add(UploadTransfer(id: record.id, connection: connection.id, name: record.name,
                destination: "Google Drive", total: record.size, job: record.job))
        }
    }
    private func save(_ record: Record) throws {
        try JSONEncoder().encode(record).write(to: directory.appendingPathComponent(record.id.uuidString + ".json"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
    private func remove(_ record: Record) {
        if let cacheDirectory = (try? self.directory) {
            try? FileManager.default.removeItem(at: cacheDirectory.appendingPathComponent(record.id.uuidString + ".json"))
            try? FileManager.default.removeItem(at: cacheDirectory.appendingPathComponent(record.id.uuidString + ".data"))
        }
        pending.removeAll { $0.id == record.id }
        UploadActivity.shared.remove(record.id)
    }
    func retry() {
        guard task == nil else { return }
        Task {
            if let record = pending.first, record.cancelRequested != true, let job = record.job {
                let client = CompanionClient(connection: connection); defer { client.close() }
                do { _ = try await client.request("jobs/" + job + "/retry", method: "POST") }
                catch { self.error = error.localizedDescription; return }
            }
            start()
        }
    }
    func cancel() { explicitCancel = true; task?.cancel() }
    private func start() {
        guard task == nil, !pending.isEmpty else { return }
        explicitCancel = false; canRetry = false; error = nil; busy = true
        task = Task {
            defer { task = nil; busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            while var record = pending.first {
                name = record.name; phase = "Uploading to server"; progress = 0
                UploadActivity.shared.update(record.id, progress: 0, state: phase)
                do {
                    if record.cancelRequested == true {
                        if let session = record.session { _ = try await client.request("drive/uploads/" + session + "/cancel", method: "POST") }
                        remove(record); continue
                    }
                    struct Create: Encodable { let name: String; let parent: String; let size: Int64 }
                    if record.session == nil {
                        let data = try await client.request("drive/uploads", method: "POST", body: JSONEncoder().encode(Create(name: record.name, parent: record.parent, size: record.size)), key: record.key)
                        record.session = try JSONDecoder().decode(DriveUpload.self, from: data).id
                        try save(record); pending[0] = record
                    }
                    guard let session = record.session else { throw URLError(.badServerResponse) }
                    var upload = try JSONDecoder().decode(DriveUpload.self, from: await client.request("drive/uploads/" + session))
                    let payload = try directory.appendingPathComponent(record.id.uuidString + ".data")
                    let file = try FileHandle(forReadingFrom: payload); defer { try? file.close() }
                    while upload.received < record.size && upload.jobId == nil {
                        try Task.checkCancellation()
                        try file.seek(toOffset: UInt64(upload.received))
                        guard let chunk = try file.read(upToCount: Int(min(4 * 1024 * 1024, record.size - upload.received))), !chunk.isEmpty else { throw CocoaError(.fileReadCorruptFile) }
                        let start = upload.received
                        let data = try await client.request("drive/uploads/" + session, method: "PUT", body: chunk,
                                                            headers: ["Content-Type": "application/octet-stream", "Content-Range": "bytes \(start)-\(start + Int64(chunk.count) - 1)/\(record.size)"])
                        upload = try JSONDecoder().decode(DriveUpload.self, from: data)
                        progress = record.size == 0 ? 1 : Double(upload.received) / Double(record.size)
                        UploadActivity.shared.update(record.id, progress: progress, state: phase)
                    }
                    guard let jobID = upload.jobId else { throw URLError(.badServerResponse) }
                    record.job = jobID; try save(record); pending[0] = record
                    phase = "Copying to Google Drive"; progress = 0
                    UploadActivity.shared.update(record.id, progress: 0, state: phase, job: jobID)
                    while true {
                        try Task.checkCancellation()
                        let job = try JSONDecoder().decode(CopyJob.self, from: await client.request("jobs/" + jobID))
                        if job.state == "completed" { break }
                        if !job.active { throw NSError(domain: "fileBeans.Transfer", code: 1, userInfo: [NSLocalizedDescriptionKey: job.error ?? "Transfer cancelled"]) }
                        phase = job.state == "waiting" ? "Waiting to retry Google Drive" : "Copying to Google Drive"
                        progress = job.total > 0 ? Double(job.done) / Double(job.total) : 0
                        UploadActivity.shared.update(record.id, progress: progress, state: phase)
                        try await Task.sleep(for: .seconds(2))
                    }
                    remove(record)
                } catch {
                    if explicitCancel {
                        record.cancelRequested = true
                        do {
                            try save(record); pending[0] = record
                            if let session = record.session {
                                let cleanup = Task {
                                    let cleanupClient = CompanionClient(connection: connection); defer { cleanupClient.close() }
                                    _ = try await cleanupClient.request("drive/uploads/" + session + "/cancel", method: "POST")
                                }
                                try await cleanup.value
                            }
                            remove(record); self.error = nil; canRetry = !pending.isEmpty
                        } catch {
                            self.error = "Cancellation is pending: " + error.localizedDescription
                            canRetry = true
                        }
                    } else { self.error = error.localizedDescription; canRetry = true; UploadActivity.shared.update(record.id, state: "Failed", error: error.localizedDescription) }
                    return
                }
            }
            phase = ""; progress = 0
        }
    }
}
