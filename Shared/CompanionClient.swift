import Foundation
import CryptoKit

struct CompanionCapabilities: Decodable {
    struct Root: Decodable { let source: String; let trash: String }
    let version: Int
    let serverId: String?
    let driveTransferKey: String?
    let roots: [Root]
    let driveConnected: Bool
    let normalizationEnabled: Bool?
    let driveWriteAccess: Bool?
    let features: [String]?
}
struct CopyLocation: Codable {
    let source: String
    var path: String
    var name: String? = nil
    var server: String? = nil
    var displayLocation: String { "\(source): \(name ?? (path.isEmpty || path == "." ? "/" : path))" }
}
struct BackgroundCopy: Encodable {
    let items: [CopyLocation]
    var destination: CopyLocation
    var operation: String? = nil
    var destinationName: String? = nil
    var driveAccount: String? = nil
    var driveGrant: SealedGoogleDriveGrant? = nil
}
struct CopyJob: Codable, Identifiable {
    struct Request: Codable {
        let items: [CopyLocation]
        let destination: CopyLocation
        let operation: String?
        let destinationName: String?
        let driveAccount: String?
    }
    var id: String
    var state: String
    let total: Int64
    let done: Int64
    let speed: Double
    let current: String
    var error: String?
    let progressUnit: String?
    let itemsTotal: Int?
    let itemsDone: Int?
    let request: Request?
    let cancel: Bool?
    let canCancel: Bool?
    var title: String {
        if let items = request?.items, !items.isEmpty {
            return items.map { $0.name ?? ($0.path as NSString).lastPathComponent }.joined(separator: ", ")
        }
        return current.isEmpty ? (request?.operation == "move" ? "Server move" : "Server copy") : current.replacingOccurrences(of: "publish:", with: "Finishing ")
    }
    var active: Bool { !["completed", "cancelled", "failed"].contains(state) }
}
struct TrashEntry: Decodable, Identifiable {
    let id: String
    let source: String
    let original: String
    let name: String
    let deleted: Int64
    let expires: Int64
    let state: String
    let error: String?
}

/// Shared with File Provider for protected local deletion only. Drive models and
/// enumeration live exclusively in App and never enter the shared item catalog.
final class CompanionClient: NSObject, URLSessionTaskDelegate {
    let connection: Connection
    // The app can defer reconnecting until foreground activation. Extensions
    // leave this unset and use the same bounded transport retry immediately.
    var prepareForJobRetry: (() async throws -> Void)?
    private let credential: String?
    private let configuration: URLSessionConfiguration
    private let store: SharedStore?
    private lazy var googleTransport = GoogleDriveTransport(connection: connection, configuration: configuration)
    private lazy var session: URLSession = {
        let config = configuration.copy() as! URLSessionConfiguration
        config.httpCookieStorage = nil
        config.urlCache = nil
        config.timeoutIntervalForRequest = 30
        return URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }()
    private lazy var mediaSession = MediaRangeTransport(configuration: configuration, connection: connection)
    init(connection: Connection, credential: String? = nil, configuration: URLSessionConfiguration = .ephemeral,
         store: SharedStore? = nil) {
        self.connection = connection.unified; self.credential = credential; self.configuration = configuration
        self.store = store
    }
    func close() { session.invalidateAndCancel(); mediaSession.close(); if connection.isGoogleDrive { googleTransport.close() } }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        ServerTrust.handle(challenge, connection: connection, completion: completionHandler)
    }
    func authorizedRequest(_ route: String, method: String = "GET", body: Data? = nil,
                           key: String? = nil, permanent: Bool = false, query: [String: String] = [:],
                           headers: [String: String] = [:]) throws -> URLRequest {
        if connection.isGoogleDrive {
            let parts = route.split(separator: "/").map(String.init)
            if method == "PUT", parts.count == 3, parts[0] == "drive", parts[1] == "uploads" {
                return try GoogleDriveUploads.authorized(connection: connection, id: parts[2], headers: headers)
            }
            return try googleTransport.authorized(route, method: method, headers: headers)
        }
        guard let configured = connection.companionURL, let tokenID = connection.companionTokenID else { throw APIError.authentication }
        let base = try FilesystemClient.validateURL(configured.absoluteString)
        var parts = URLComponents(url: base.appendingPathComponent("v1/" + route), resolvingAgainstBaseURL: false)!
        parts.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        parts.percentEncodedQuery = parts.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        var r = URLRequest(url: parts.url!)
        r.httpMethod = method; r.httpBody = body
        if route.hasPrefix("protection/") { r.timeoutInterval = 60 }
        r.setValue("Bearer " + (try credential ?? TokenStore.read(tokenID)), forHTTPHeaderField: "Authorization")
        if body != nil { r.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        if let key { r.setValue(key, forHTTPHeaderField: "Idempotency-Key") }
        if permanent { r.setValue("yes", forHTTPHeaderField: "X-Confirm-Permanent") }
        for (name, value) in headers { r.setValue(value, forHTTPHeaderField: name) }
        return r
    }
    func request(_ route: String, method: String = "GET", body: Data? = nil,
                 key: String? = nil, permanent: Bool = false, query: [String: String] = [:],
                 headers: [String: String] = [:]) async throws -> Data {
        if let (owner, forwarded, alias) = try ClientTransferLinks.resolve(route, origin: connection) {
            let client = CompanionClient(connection: owner); defer { client.close() }
            let data = try await client.request(forwarded, method: method, body: body, key: key, permanent: permanent, query: query, headers: headers)
            var job = try JSONDecoder().decode(CopyJob.self, from: data); job.id = alias
            return try JSONEncoder().encode(job)
        }
        if connection.isGoogleDrive {
            return try await googleTransport.request(route, method: method, body: body, key: key, permanent: permanent, query: query, headers: headers)
        }
        let r = try authorizedRequest(route, method: method, body: body, key: key, permanent: permanent, query: query, headers: headers)
        let (data, response) = try await ServerRoute.shared.data(r, connection: connection, session: session)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            if http.statusCode == 401 { throw APIError.authentication }
            if let detail = try? JSONDecoder().decode(ServiceError.self, from: data) {
                throw NSError(domain: "fileBeans.Companion", code: http.statusCode,
                              userInfo: [NSLocalizedDescriptionKey: detail.error])
            }
            throw APIError.status(http.statusCode)
        }
        return data
    }
    func streamChunk(_ request: URLRequest, offset: Int64, length: Int) async throws -> MediaChunk {
        let prepared = connection.isGoogleDrive ? try await googleTransport.refreshed(request) : request
        return try await mediaSession.chunk(prepared, offset: offset, length: length)
    }
    func streamChunks(_ request: URLRequest, offset: Int64, length: Int) -> AsyncThrowingStream<MediaChunk, Error> {
        guard connection.isGoogleDrive else { return mediaSession.chunks(request, offset: offset, length: length) }
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let prepared = try await googleTransport.refreshed(request)
                    for try await chunk in mediaSession.chunks(prepared, offset: offset, length: length) { continuation.yield(chunk) }
                    continuation.finish()
                } catch { continuation.finish(throwing: error) }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }
    func download(_ route: String) async throws -> URL {
        if connection.isGoogleDrive { return try await googleTransport.download(route) }
        let (file, response) = try await session.download(for: ServerRoute.shared.prepare(authorizedRequest(route), connection: connection))
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            try? FileManager.default.removeItem(at: file)
            throw APIError.status((response as? HTTPURLResponse)?.statusCode ?? 0)
        }
        let target = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.moveItem(at: file, to: target)
        return target
    }
    private struct ServiceError: Decodable { let error: String }
    func capabilities() async throws -> CompanionCapabilities {
        let c = try JSONDecoder().decode(CompanionCapabilities.self, from: await request("capabilities"))
        guard c.version == 1 else { throw APIError.incompatible }; return c
    }
    func submit(_ copy: BackgroundCopy, key: String? = nil, googleDrive: Connection? = nil) async throws -> CopyJob {
        let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys
        var canonical = BackgroundCopy(items: try copy.items.map(sourceLocation).sorted {
            $0.source == $1.source ? $0.path < $1.path : $0.source < $1.source
        }, destination: try sourceLocation(copy.destination), operation: copy.operation, destinationName: copy.destinationName, driveAccount: googleDrive?.googleAccountID ?? copy.driveAccount)
        let identityBody = try encoder.encode(canonical)
        let identity = Data((connection.id.uuidString + (connection.companionURL?.absoluteString ?? "")).utf8) + identityBody
        let hash = SHA256.hash(data: identity).map { String(format: "%02x", $0) }.joined()
        let directory = try (store?.directory ?? AppConfiguration.directory).appendingPathComponent("copy-submissions", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let pending = directory.appendingPathComponent(hash + ".key")
        // A retry after a lost response or app restart uses the same request key.
        let saved = try? String(contentsOf: pending, encoding: .utf8)
        let requestKey = saved ?? key ?? UUID().uuidString
        if saved == nil { try Data(requestKey.utf8).write(to: pending, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication]) }
        do {
            if let googleDrive {
                let server = try await capabilities()
                canonical.driveGrant = try await GoogleDriveDelegation.seal(account: googleDrive, server: server)
            }
            let body = try encoder.encode(canonical)
            let job = try JSONDecoder().decode(CopyJob.self, from: await workRequest("jobs", method: "POST", body: body, key: requestKey))
            try? FileManager.default.removeItem(at: pending)
            return job
        } catch {
            let failure = error as NSError
            if failure.domain == "fileBeans.Companion", [400, 401, 403, 404, 409].contains(failure.code) {
                try? FileManager.default.removeItem(at: pending)
            }
            throw error
        }
    }
    func waitForMove(_ submitted: CopyJob) async throws {
        var job = submitted
        while job.active {
            try await Task.sleep(nanoseconds: 1_000_000_000)
            job = try JSONDecoder().decode(CopyJob.self, from: await workRequest("jobs/\(job.id)"))
        }
        guard job.state == "completed" else {
            throw NSError(domain: "fileBeans.Companion", code: 409,
                          userInfo: [NSLocalizedDescriptionKey: job.error.flatMap { $0.isEmpty ? nil : $0 } ?? "Move failed"])
        }
    }
    func workRequest(_ route: String, method: String = "GET", body: Data? = nil, key: String? = nil) async throws -> Data {
        do {
            return try await request(route, method: method, body: body, key: key)
        } catch {
            let failure = error as NSError
            let reconnectable: Set<Int> = [URLError.timedOut.rawValue, URLError.networkConnectionLost.rawValue,
                URLError.notConnectedToInternet.rawValue, URLError.cannotConnectToHost.rawValue,
                URLError.cannotFindHost.rawValue, URLError.dnsLookupFailed.rawValue]
            guard !Task.isCancelled, failure.domain == NSURLErrorDomain, reconnectable.contains(failure.code) else { throw error }
            try await prepareForJobRetry?()
            try Task.checkCancellation()
            // A submission replay must use the exact same body and key. The
            // server returns the existing job, including its completed state.
            return try await request(route, method: method, body: body, key: key)
        }
    }
    func jobs() async throws -> [CopyJob] {
        let local = try JSONDecoder().decode([CopyJob].self, from: await workRequest("jobs"))
        return local + (try await ClientTransferLinks.jobs(origin: connection))
    }
    func trash(_ item: StoredItem) async throws {
        let location = try sourceLocation(CopyLocation(source: item.source, path: item.file.path))
        _ = try await request("trash", method: "POST", body: JSONEncoder().encode(location), key: UUID().uuidString)
    }
    func sourceLocation(_ location: CopyLocation) throws -> CopyLocation {
        guard location.source != "drive", location.source != "upload" else { return location }
        if let owner = location.server, owner != connection.serverID { return location }
        let scope = try (store ?? SharedStore()).sourceScope(connection.id, source: location.source)
        return CopyLocation(source: location.source,
                            path: try FilePath.inSource(location.path, scope: scope), name: location.name, server: location.server)
    }
    func trashEntries(source: String? = nil) async throws -> [TrashEntry] {
        try JSONDecoder().decode([TrashEntry].self, from: await request("trash", query: source.map { ["source": $0] } ?? [:]))
    }
    static func protected(_ path: String) -> Bool {
        FilePath.contains(path, in: "/Trash") || path.split(separator: "/").contains {
            [".filebeans-stage", ".filebeans-vaults", ".filebeans-thumbnails"].contains($0.lowercased())
        }
    }
}
