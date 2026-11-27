import Foundation

private struct ClientTransferLink: Codable {
    let owner: UUID
    let job: String
    var last: CopyJob? = nil
}

/// Client-owned references to transfers explicitly started between configured
/// sources. Servers never aggregate each other's jobs or discover client sources.
enum ClientTransferLinks {
    private static func url(_ origin: UUID) throws -> URL {
        let directory = try AppConfiguration.directory.appendingPathComponent("transfer-links")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory.appendingPathComponent(origin.uuidString + ".json")
    }
    private static let lock = NSLock()
    private static func read(_ origin: UUID) throws -> [String: ClientTransferLink] {
        let file = try url(origin)
        guard FileManager.default.fileExists(atPath: file.path) else { return [:] }
        let data = try Data(contentsOf: file)
        guard data.count <= 4 * 1024 * 1024 else { throw APIError.incompatible }
        return try JSONDecoder().decode([String: ClientTransferLink].self, from: data)
    }
    static func register(_ job: CopyJob, origin: Connection, owner: Connection) throws -> CopyJob {
        guard origin.id != owner.id else { return job }
        var linked = job
        linked.id = owner.id.uuidString + "~" + job.id
        lock.lock(); defer { lock.unlock() }
        var records = try read(origin.id)
        guard records.count < 1024 || records[linked.id] != nil else { throw APIError.forbidden }
        records[linked.id] = ClientTransferLink(owner: owner.id, job: job.id, last: linked)
        let data = try JSONEncoder().encode(records)
        guard data.count <= 4 * 1024 * 1024 else { throw APIError.forbidden }
        try data.write(to: url(origin.id), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        return linked
    }
    static func resolve(_ route: String, origin: Connection) throws -> (Connection, String, String)? {
        let parts = route.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        guard parts.count >= 2, parts[0] == "jobs", let split = parts[1].firstIndex(of: "~"),
              UUID(uuidString: String(parts[1][..<split])) != nil else { return nil }
        lock.lock(); let link: ClientTransferLink?
        do { link = try read(origin.id)[parts[1]]; lock.unlock() } catch { lock.unlock(); throw error }
        guard let link, let owner = try SharedStore().connections().first(where: { $0.id == link.owner }),
              !owner.isGoogleDrive else { throw APIError.missing }
        let suffix = parts.dropFirst(2).joined(separator: "/")
        return (owner, "jobs/" + link.job + (suffix.isEmpty ? "" : "/" + suffix), parts[1])
    }
    private static func snapshots(_ origin: UUID) throws -> [String: ClientTransferLink] {
        lock.lock(); defer { lock.unlock() }; return try read(origin)
    }
    static func jobs(origin: Connection) async throws -> [CopyJob] {
        let records = try snapshots(origin.id)
        var jobs: [CopyJob] = []
        for (id, link) in records {
            do {
                guard let (owner, route, alias) = try resolve("jobs/" + id, origin: origin) else { continue }
                let client = CompanionClient(connection: owner); defer { client.close() }
                var job = try JSONDecoder().decode(CopyJob.self, from: await client.request(route))
                job.id = alias; jobs.append(job)
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
