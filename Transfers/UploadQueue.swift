import SwiftUI

struct QueuedUpload: Codable, Identifiable {
    let id: UUID
    let connection: UUID
    let name: String
    let source: String // "drive" is an app-only destination, never a provider source.
    let parent: String
    let size: Int64
    let created: Date
    let owner: String
    var session: String?
    var job: String?
    var cancelled = false
    var completed = false
    var failure: String?
    var received: Int64 = 0
    var legacyKey: String? = nil
}
struct FilesystemUploadState: Decodable {
    let id: String
    let size: Int64
    let received: Int64
    let completed: Bool
}

/// Records and file-backed chunk tasks survive view dismissal and process exit.
/// Only task descriptions contain record IDs; credentials stay in request headers.
@MainActor
final class UploadQueue: NSObject, ObservableObject, URLSessionTaskDelegate {
    static let shared = UploadQueue()
    @Published private(set) var records: [QueuedUpload] = []
    private var sessions: [String: URLSession] = [:]
    private var preparing = Set<UUID>()
    private var restoring = false
    private var polling = Set<UUID>()
    private var tasks: [UUID: URLSessionTask] = [:]
    private var completions: [String: () -> Void] = [:]
    private var eventWaiters = Set<String>()
    static var sessionPrefix: String { AppConfiguration.group + ".uploads." }
    private var owner: String { Bundle.main.bundleIdentifier?.hasSuffix(".Share") == true ? "share" : "app" }
    private var directory: URL { get throws { try AppConfiguration.directory.appendingPathComponent("upload-queue", isDirectory: true) } }
    private func file(_ id: UUID, _ suffix: String) throws -> URL { try directory.appendingPathComponent(id.uuidString + suffix) }
    private func background(_ owner: String) -> URLSession {
        if let session = sessions[owner] { return session }
        let config = URLSessionConfiguration.background(withIdentifier: Self.sessionPrefix + owner)
        config.sharedContainerIdentifier = AppConfiguration.group
        config.sessionSendsLaunchEvents = true
        config.isDiscretionary = false
        config.httpCookieStorage = nil; config.urlCache = nil
        let session = URLSession(configuration: config, delegate: self, delegateQueue: .main)
        sessions[owner] = session
        return session
    }
    private func save(_ value: QueuedUpload) throws {
        var record = value
        if records.first(where: { $0.id == value.id })?.cancelled == true { record.cancelled = true }
        let url = try file(record.id, ".json")
        let coordinator = NSFileCoordinator(filePresenter: nil)
        var coordinationError: NSError?
        var result: Result<Void, Error>?
        coordinator.coordinate(writingItemAt: url, options: .forReplacing, error: &coordinationError) { target in
            result = Result {
                if let data = try? Data(contentsOf: target), let stored = try? JSONDecoder().decode(QueuedUpload.self, from: data), stored.cancelled { record.cancelled = true }
                try JSONEncoder().encode(record).write(to: target, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            }
        }
        if let result { try result.get() } else { throw coordinationError ?? CocoaError(.fileWriteUnknown) as NSError }
        if let index = records.firstIndex(where: { $0.id == record.id }) { records[index] = record }
        else { records.append(record) }
        publish(record)
    }
    private func publish(_ record: QueuedUpload) {
        UploadActivity.shared.add(UploadTransfer(id: record.id, connection: record.connection, name: record.name,
            destination: record.source + ": " + record.parent, total: record.size, job: record.job))
        UploadActivity.shared.update(record.id, progress: record.size == 0 ? 0 : Double(record.received) / Double(record.size),
            state: record.cancelled ? "Cancelling" : record.failure != nil ? "Paused" : record.job != nil ? "Copying to Google Drive" : "Uploading",
            error: record.failure, job: record.job)
    }
    func enqueue(_ urls: [URL], connection: Connection, source: String, parent: String, scoped: Bool = true) async throws {
        let directory = try directory
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        for url in urls {
            _ = try FilePath.child(url.lastPathComponent, in: "/")
            let access = scoped && url.startAccessingSecurityScopedResource()
            defer { if access { url.stopAccessingSecurityScopedResource() } }
            let id = UUID(), destination = try file(id, ".data")
            do {
                let size = try await Task.detached(priority: .userInitiated) {
                    try FileManager.default.copyItem(at: url, to: destination)
                    try FileManager.default.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: destination.path)
                    return Int64(try destination.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0)
                }.value
                let record = QueuedUpload(id: id, connection: connection.id, name: url.lastPathComponent,
                    source: source, parent: parent, size: size, created: Date(), owner: owner)
                try save(record)
                schedule(id)
            } catch { try? FileManager.default.removeItem(at: destination); throw error }
        }
        recover()
    }
    func waitForScheduling() async {
        while restoring || records.contains(where: { preparing.contains($0.id) && $0.job == nil }) {
            do { try await Task.sleep(for: .milliseconds(100)) } catch { return }
        }
    }
    func handleEvents(identifier: String, completion: @escaping () -> Void) {
        guard identifier.hasPrefix(Self.sessionPrefix) else { completion(); return }
        completions[identifier] = completion
        _ = background(String(identifier.dropFirst(Self.sessionPrefix.count)))
        recover()
    }
    func recover() {
        guard !restoring else { return }
        restoring = true
        Task {
            defer { restoring = false; finishEventsIfReady() }
            do {
                let directory = try directory
                try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                try migrateDriveUploads()
                records = try FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
                    .filter { $0.pathExtension == "json" }.compactMap { try? JSONDecoder().decode(QueuedUpload.self, from: Data(contentsOf: $0)) }
                    .sorted { $0.created < $1.created }
                // Reattach first; never duplicate a chunk already owned by URLSession.
                let owners = owner == "share" ? ["share"] : ["app", "share"]
                for name in owners {
                    let session = background(name)
                    let existing = await session.allTasks
                    for task in existing {
                        if let text = task.taskDescription, let id = UUID(uuidString: text) { tasks[id] = task }
                    }
                }
                for var record in records {
                    // A foreground recovery retries interrupted transfers once.
                    if tasks[record.id] == nil && !preparing.contains(record.id) {
                        record.failure = nil; try save(record)
                    }
                    publish(record)
                }
                pump()
            } catch { /* Persisted records remain available for the next recovery. */ }
        }
    }
    private func migrateDriveUploads() throws {
        struct Legacy: Decodable {
            let id: UUID; let name: String; let parent: String; let size: Int64
            let created: Date; let key: String; let session: String?; let job: String?; let cancelRequested: Bool?
        }
        guard owner == "app" else { return }
        let base = try AppConfiguration.directory.appendingPathComponent("drive-uploads")
        for folder in (try? FileManager.default.contentsOfDirectory(at: base, includingPropertiesForKeys: nil)) ?? [] {
            guard let connection = UUID(uuidString: folder.lastPathComponent) else { continue }
            for url in (try? FileManager.default.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)) ?? [] where url.pathExtension == "json" {
                let legacy = try JSONDecoder().decode(Legacy.self, from: Data(contentsOf: url))
                let target = try file(legacy.id, ".data")
                let old = folder.appendingPathComponent(legacy.id.uuidString + ".data")
                if !FileManager.default.fileExists(atPath: target.path) { try FileManager.default.copyItem(at: old, to: target) }
                let manifest = try file(legacy.id, ".json")
                if !FileManager.default.fileExists(atPath: manifest.path) {
                    var record = QueuedUpload(id: legacy.id, connection: connection, name: legacy.name, source: "drive",
                        parent: legacy.parent, size: legacy.size, created: legacy.created, owner: "app")
                    record.session = legacy.session; record.job = legacy.job; record.cancelled = legacy.cancelRequested == true
                    record.legacyKey = legacy.key
                    try save(record)
                }
                try FileManager.default.removeItem(at: url)
                try? FileManager.default.removeItem(at: old)
            }
        }
    }
    func retry(_ id: UUID) {
        guard var record = records.first(where: { $0.id == id }) else { return }
        Task {
            do {
                if let job = record.job,
                   let connection = try SharedStore().connections().first(where: { $0.id == record.connection }) {
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    _ = try await client.request("jobs/" + job + "/retry", method: "POST")
                }
                record.failure = nil; try save(record); schedule(id)
            } catch {
                record.failure = error.localizedDescription; try? save(record)
            }
        }
    }
    func cancel(_ id: UUID) {
        guard var record = records.first(where: { $0.id == id }) else { return }
        record.cancelled = true; record.failure = nil
        do { try save(record) } catch { return }
        if let task = tasks[id] { task.cancel() } else { schedule(id) }
    }
    private func remove(_ record: QueuedUpload) {
        if let directory = (try? self.directory) {
            let files = (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)) ?? []
            for url in files where url.lastPathComponent.hasPrefix(record.id.uuidString + ".") {
                try? FileManager.default.removeItem(at: url)
            }
        }
        records.removeAll { $0.id == record.id }; UploadActivity.shared.remove(record.id)
    }
    private func pump() {
        for record in records where record.failure == nil && (owner == "app" || record.owner == owner) { schedule(record.id) }
    }
    private func schedule(_ id: UUID) {
        guard tasks.count + preparing.count < 2,
              tasks[id] == nil, !preparing.contains(id), !polling.contains(id) else { return }
        preparing.insert(id)
        Task {
            var again = false
            defer {
                preparing.remove(id)
                if again { schedule(id) }
                pump()
                finishEventsIfReady()
            }
            guard var record = records.first(where: { $0.id == id }) else { return }
            do {
                guard let connection = try SharedStore().connections().first(where: { $0.id == record.connection })?.unified else { throw APIError.authentication }
                let client = CompanionClient(connection: connection); defer { client.close() }
                let path = try FilePath.child(record.name, in: record.parent)
                let query = ["source": record.source, "path": path]
                let route = record.source == "drive" ? "drive/uploads/" : "fs/uploads/"
                if record.cancelled || record.completed {
                    if let session = record.session {
                        do {
                            _ = try await client.request(route + session + (record.source == "drive" ? "/cancel" : ""),
                                method: record.source == "drive" ? "POST" : "DELETE", query: record.source == "drive" ? [:] : query)
                        } catch let error as NSError {
                            guard error.code == 404 else { throw error }
                        }
                    }
                    remove(record); return
                }
                var received: Int64
                if record.source == "drive" {
                    if record.session == nil {
                        struct Create: Encodable { let name: String; let parent: String; let size: Int64 }
                        let data = try await client.request("drive/uploads", method: "POST", body: JSONEncoder().encode(Create(name: record.name, parent: record.parent, size: record.size)), key: record.legacyKey ?? record.id.uuidString)
                        record.session = try JSONDecoder().decode(DriveUpload.self, from: data).id
                        try save(record)
                    }
                    let data = try await client.request(route + record.session!)
                    let state = try JSONDecoder().decode(DriveUpload.self, from: data)
                    received = state.received; record.job = state.jobId
                    if let job = state.jobId {
                        try save(record)
                        let status = try JSONDecoder().decode(CopyJob.self, from: await client.request("jobs/" + job))
                        if status.state == "completed" { remove(record); return }
                        if !status.active { throw NSError(domain: "fileBeans.Upload", code: 1, userInfo: [NSLocalizedDescriptionKey: status.error ?? "Transfer stopped"]) }
                        // Server copies continue independently. Reconcile while active,
                        // or on the next foreground if iOS suspends this process.
                        polling.insert(id)
                        Task { @MainActor in
                            try? await Task.sleep(for: .seconds(2))
                            polling.remove(id); schedule(id)
                        }
                        return
                    }
                } else {
                    guard (try await client.capabilities()).features?.contains("resumableUploadsV1") == true else { throw APIError.incompatible }
                    if record.session == nil {
                        // Persist the client-generated ID before sending the create request.
                        record.session = record.id.uuidString; try save(record)
                    }
                    struct Create: Encodable { let size: Int64 }
                    let data = try await client.request(route + record.session!, method: "POST", body: JSONEncoder().encode(Create(size: record.size)), query: query)
                    let state = try JSONDecoder().decode(FilesystemUploadState.self, from: data)
                    received = state.received
                    if state.completed || record.completed {
                        record.completed = true; try save(record)
                        _ = try await client.request(route + record.session!, method: "DELETE", query: query)
                        remove(record); try? Repository(connection: connection, store: SharedStore()).notify(); return
                    }
                    if received == record.size {
                        _ = try await client.request(route + record.session! + "/complete", method: "POST", query: query)
                        record.completed = true; try save(record)
                        _ = try await client.request(route + record.session!, method: "DELETE", query: query)
                        remove(record); try? Repository(connection: connection, store: SharedStore()).notify(); return
                    }
                }
                guard received >= 0, received < record.size else { throw URLError(.badServerResponse) }
                // Re-read cancellation after every await; cancellation must not be lost.
                if records.first(where: { $0.id == id })?.cancelled == true {
                    record.cancelled = true; try save(record); again = true; return
                }
                record.received = received; record.failure = nil; try save(record)
                let payload = try FileHandle(forReadingFrom: file(id, ".data")); defer { try? payload.close() }
                try payload.seek(toOffset: UInt64(received))
                let count = Int(min(4 * 1024 * 1024, record.size - received))
                guard let chunk = try payload.read(upToCount: count), chunk.count == count else { throw CocoaError(.fileReadCorruptFile) }
                let chunkFile = try file(id, "." + String(received) + ".chunk")
                try chunk.write(to: chunkFile, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
                let request = try client.authorizedRequest(route + record.session!, method: "PUT", query: record.source == "drive" ? [:] : query,
                    headers: ["Content-Type": "application/octet-stream", "Content-Range": "bytes \(received)-\(received + Int64(count) - 1)/\(record.size)"])
                let task = background(record.owner).uploadTask(with: request, fromFile: chunkFile)
                task.taskDescription = id.uuidString; tasks[id] = task; task.resume()
            } catch {
                if var latest = records.first(where: { $0.id == id }) {
                    latest.failure = error.localizedDescription; try? save(latest)
                }
            }
        }
    }
    nonisolated func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        Task { @MainActor in
            guard let text = task.taskDescription, let id = UUID(uuidString: text) else { return }
            tasks.removeValue(forKey: id)
            if let range = task.originalRequest?.value(forHTTPHeaderField: "Content-Range"),
               let offset = range.dropFirst(6).split(separator: "-").first,
               let start = Int64(offset), let chunk = try? file(id, "." + String(start) + ".chunk") {
                try? FileManager.default.removeItem(at: chunk)
            }
            let code = (task.response as? HTTPURLResponse)?.statusCode ?? 0
            if error == nil && ((200..<300).contains(code) || code == 409) || records.first(where: { $0.id == id })?.cancelled == true {
                schedule(id)
            } else if var record = records.first(where: { $0.id == id }) {
                record.failure = error?.localizedDescription ?? APIError.status(code).localizedDescription
                try? save(record)
            }
            pump()
            finishEventsIfReady()
        }
    }
    nonisolated func urlSession(_ session: URLSession, task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
    nonisolated func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        Task { @MainActor in
            if let id = session.configuration.identifier { eventWaiters.insert(id) }
            finishEventsIfReady()
        }
    }
    private func finishEventsIfReady() {
        guard preparing.isEmpty, !restoring else { return }
        for identifier in eventWaiters {
            completions.removeValue(forKey: identifier)?()
        }
        eventWaiters.removeAll()
    }
}
