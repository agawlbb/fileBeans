import Foundation
import CryptoKit

private struct GoogleCopyRequest: Codable {
    let items: [CopyLocation]
    let destination: CopyLocation
    let operation: String?
    let destinationName: String?
}
private struct GoogleCopyJob: Codable {
    let id: String
    var state = "queued"
    var total: Int64
    var done: Int64 = 0
    var speed: Double = 0
    var current = ""
    var error: String? = nil
    var cancel = false
    var progressUnit = "items"
    var itemsTotal: Int
    var itemsDone = 0
    let request: GoogleCopyRequest
}

/// Google performs native copies and parent updates. The app journals only
/// metadata and resumes folder traversal; it never downloads copy payloads.
actor GoogleDriveJobs {
    static let shared = GoogleDriveJobs()
    private var tasks: [String: Task<Void, Never>] = [:]
    private func directory(_ connection: Connection) throws -> URL {
        let url = try AppConfiguration.directory.appendingPathComponent("google-jobs/" + connection.id.uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
    private func read(_ id: String, _ connection: Connection) throws -> GoogleCopyJob {
        _ = try GoogleDriveTransport.id(id)
        let url = try directory(connection).appendingPathComponent(id + ".json")
        let data = try Data(contentsOf: url)
        guard data.count < 4 * 1024 * 1024 else { throw APIError.incompatible }
        return try JSONDecoder().decode(GoogleCopyJob.self, from: data)
    }
    private func save(_ job: GoogleCopyJob, _ connection: Connection) throws {
        let data = try JSONEncoder().encode(job)
        guard data.count < 4 * 1024 * 1024 else { throw APIError.incompatible }
        try data.write(to: directory(connection).appendingPathComponent(job.id + ".json"),
                       options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
    private func list(_ connection: Connection) throws -> [GoogleCopyJob] {
        try FileManager.default.contentsOfDirectory(at: directory(connection), includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "json" }.map { try read($0.deletingPathExtension().lastPathComponent, connection) }
    }
    func recover(_ connection: Connection) throws {
        for job in try list(connection) where job.state == "running" || job.state == "queued" { start(job.id, connection) }
    }
    private func start(_ id: String, _ connection: Connection) {
        let key = connection.id.uuidString + ":" + id
        guard tasks[key] == nil else { return }
        tasks[key] = Task { await self.run(id, connection) }
    }
    func request(transport: GoogleDriveTransport, route: String, method: String, body: Data?, key: String?) async throws -> Data {
        let connection = transport.connection
        if route == "jobs", method == "GET" {
            try recover(connection)
            return try JSONEncoder().encode(list(connection))
        }
        if route == "jobs", method == "POST" {
            guard let key, !key.isEmpty, let body, body.count < 1024 * 1024 else { throw APIError.invalidURL }
            let request = try JSONDecoder().decode(GoogleCopyRequest.self, from: body)
            guard !request.items.isEmpty, request.items.count <= 100,
                  request.destination.source == "drive", request.items.allSatisfy({ $0.source == "drive" }),
                  request.operation == nil || request.operation == "copy" || request.operation == "move" else { throw APIError.incompatible }
            _ = try GoogleDriveTransport.id(request.destination.path)
            for item in request.items { _ = try GoogleDriveTransport.id(item.path) }
            let id = SHA256.hash(data: Data(key.utf8)).map { String(format: "%02x", $0) }.joined()
            if let old = try? read(id, connection) {
                let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys
                guard try encoder.encode(old.request) == encoder.encode(request) else { throw APIError.forbidden }
                return try JSONEncoder().encode(old)
            }
            let job = GoogleCopyJob(id: id, total: Int64(request.items.count), itemsTotal: request.items.count, request: request)
            try save(job, connection); start(id, connection)
            return try JSONEncoder().encode(job)
        }
        let parts = route.split(separator: "/").map(String.init)
        guard parts.count >= 2, parts[0] == "jobs" else { throw APIError.incompatible }
        var job = try read(parts[1], connection)
        if parts.count == 3, method == "POST" {
            let taskKey = connection.id.uuidString + ":" + job.id
            if parts[2] == "cancel" {
                job.cancel = true; job.state = "cancelled"; try save(job, connection); tasks[taskKey]?.cancel()
            } else if parts[2] == "retry", job.state == "failed" {
                job.state = "queued"; job.error = nil; job.cancel = false; try save(job, connection); start(job.id, connection)
            } else { throw APIError.forbidden }
        } else if parts.count != 2 || method != "GET" { throw APIError.incompatible }
        return try JSONEncoder().encode(job)
    }
    private func run(_ id: String, _ connection: Connection) async {
        let key = connection.id.uuidString + ":" + id
        defer { tasks.removeValue(forKey: key) }
        let transport = GoogleDriveTransport(connection: connection)
        defer { transport.close() }
        do {
            var job = try read(id, connection)
            guard !job.cancel else { return }
            job.state = "running"; try save(job, connection)
            let destination = try await transport.metadata(job.request.destination.path)
            guard destination["mimeType"] as? String == "application/vnd.google-apps.folder",
                  let destinationID = destination["id"] as? String else { throw APIError.forbidden }
            for (index, item) in job.request.items.enumerated() {
                try Task.checkCancellation()
                job = try read(id, connection); guard !job.cancel else { return }
                let file = try await transport.metadata(item.path)
                guard let sourceID = file["id"] as? String, let name = file["name"] as? String else { throw APIError.incompatible }
                // Resolve the destination ancestry before any change. Moving or
                // copying a folder into itself must not begin a recursive job.
                var cursor = destinationID, visited = Set<String>()
                while visited.insert(cursor).inserted {
                    guard cursor != sourceID, visited.count <= 64 else { throw APIError.forbidden }
                    let parent = try await transport.metadata(cursor)
                    guard let next = (parent["parents"] as? [String])?.first else { break }
                    cursor = next
                }
                job.current = name; try save(job, connection)
                if job.request.operation == "move" {
                    let parents = file["parents"] as? [String] ?? []
                    let old = parents.filter { $0 != destinationID }
                    let rename = job.request.destinationName.map { $0 != name } ?? false
                    if !parents.contains(destinationID) || rename {
                        var query = ["supportsAllDrives": "true"]
                        if !parents.contains(destinationID) { query["addParents"] = destinationID }
                        if !old.isEmpty { query["removeParents"] = old.joined(separator: ",") }
                        var fields: [String: Any] = [:]
                        if let name = job.request.destinationName { fields["name"] = name }
                        _ = try await transport.checked(transport.url("files/" + GoogleDriveTransport.id(sourceID), query: query),
                            method: "PATCH", body: JSONSerialization.data(withJSONObject: fields), headers: ["Content-Type": "application/json"])
                    }
                } else {
                    var count = 0
                    try await copy(file, into: destinationID, name: job.request.destinationName ?? name,
                                   operation: id, transport: transport, depth: 0, count: &count)
                }
                job = try read(id, connection)
                guard !job.cancel else { return }
                job.done = Int64(index + 1); job.itemsDone = index + 1; try save(job, connection)
            }
            job = try read(id, connection)
            if !job.cancel { job.state = "completed"; job.current = ""; try save(job, connection) }
        } catch {
            if var job = try? read(id, connection), !job.cancel {
                job.state = "failed"; job.error = error.localizedDescription; try? save(job, connection)
            }
        }
    }
    private func copy(_ file: [String: Any], into parent: String, name: String, operation: String,
                      transport: GoogleDriveTransport, depth: Int, count: inout Int) async throws {
        try Task.checkCancellation()
        count += 1; guard count <= 10_000, depth <= 64, let id = file["id"] as? String else { throw APIError.forbidden }
        if file["mimeType"] as? String != "application/vnd.google-apps.folder" {
            _ = try await transport.publishCopy(source: id, name: name, parent: parent, operation: operation + ":" + id)
            return
        }
        let target = try await transport.createFolder(name: name, parent: parent, operation: operation + ":" + id)
        var page = ""
        repeat {
            let data = try await transport.listing("'\(GoogleDriveTransport.escaped(id))' in parents and trashed=false", page: page)
            guard let result = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let files = result["files"] as? [[String: Any]] else { throw APIError.incompatible }
            for child in files {
                guard let name = child["name"] as? String else { throw APIError.incompatible }
                try await copy(child, into: target, name: name, operation: operation, transport: transport, depth: depth + 1, count: &count)
            }
            page = result["nextPageToken"] as? String ?? ""
        } while !page.isEmpty
    }
}
