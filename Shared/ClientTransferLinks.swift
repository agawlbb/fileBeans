import Foundation

private struct ClientTransferLink: Codable {
    let owner: UUID
    let job: String
    var last: CopyJob? = nil
}

/// Client-owned references to transfers explicitly started between configured
/// sources. Servers never aggregate each other's jobs or discover client sources.
enum ClientTransferLinks {
    private static func url(_ origin: UUID, store: SharedStore?) throws -> URL {
        let directory = try (store?.directory ?? AppConfiguration.directory).appendingPathComponent("transfer-links")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory.appendingPathComponent(origin.uuidString + ".json")
    }
    private static let lock = NSLock()
    private static func read(_ origin: UUID, store: SharedStore?) throws -> [String: ClientTransferLink] {
        let file = try url(origin, store: store)
        guard FileManager.default.fileExists(atPath: file.path) else { return [:] }
        let data = try Data(contentsOf: file)
        guard data.count <= 4 * 1024 * 1024 else { throw APIError.incompatible }
        return try JSONDecoder().decode([String: ClientTransferLink].self, from: data)
    }
    static func register(_ job: CopyJob, origin: Connection, owner: Connection, store: SharedStore? = nil) throws -> CopyJob {
        guard origin.id != owner.id else { return job }
        var linked = job
        linked.id = owner.id.uuidString + "~" + job.id
        lock.lock(); defer { lock.unlock() }
        var records = try read(origin.id, store: store)
        guard records.count < 1024 || records[linked.id] != nil else { throw APIError.forbidden }
        records[linked.id] = ClientTransferLink(owner: owner.id, job: job.id, last: linked)
        let data = try JSONEncoder().encode(records)
        guard data.count <= 4 * 1024 * 1024 else { throw APIError.forbidden }
        try data.write(to: url(origin.id, store: store), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        return linked
    }
    static func resolve(_ route: String, origin: Connection, store: SharedStore? = nil) throws -> (Connection, String, String)? {
        let parts = route.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        guard parts.count >= 2, parts[0] == "jobs", let split = parts[1].firstIndex(of: "~"),
              UUID(uuidString: String(parts[1][..<split])) != nil else { return nil }
        lock.lock(); let link: ClientTransferLink?
        do { link = try read(origin.id, store: store)[parts[1]]; lock.unlock() } catch { lock.unlock(); throw error }
        let catalog = try store ?? SharedStore()
        guard let link, let owner = try catalog.connections().first(where: { $0.id == link.owner }),
              !owner.isGoogleDrive else { throw APIError.missing }
        let suffix = parts.dropFirst(2).joined(separator: "/")
        return (owner, "jobs/" + link.job + (suffix.isEmpty ? "" : "/" + suffix), parts[1])
    }
    static func remember(_ job: CopyJob, origin: Connection, store: SharedStore? = nil) throws {
        lock.lock(); defer { lock.unlock() }
        var records = try read(origin.id, store: store)
        guard var link = records[job.id] else { return }
        link.last = job; records[job.id] = link
        let data = try JSONEncoder().encode(records)
        guard data.count <= 4 * 1024 * 1024 else { throw APIError.forbidden }
        try data.write(to: url(origin.id, store: store), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
    private static func snapshots(_ origin: UUID, store: SharedStore?) throws -> [String: ClientTransferLink] {
        lock.lock(); defer { lock.unlock() }; return try read(origin, store: store)
    }
    static func jobs(origin: Connection, store: SharedStore? = nil) async throws -> [CopyJob] {
        let records = try snapshots(origin.id, store: store)
        var jobs: [CopyJob] = []
        for (id, link) in records {
            do {
                guard let (owner, route, alias) = try resolve("jobs/" + id, origin: origin, store: store) else { continue }
                let client = CompanionClient(connection: owner); defer { client.close() }
                var job = try JSONDecoder().decode(CopyJob.self, from: await client.request(route))
                job.id = alias; try remember(job, origin: origin, store: store); jobs.append(job)
            } catch {
                if var last = link.last {
                    if last.active { last.state = "unavailable"; last.error = "The transfer server is unavailable." }
                    jobs.append(last)
                }
            }
        }
        return jobs
    }
}
