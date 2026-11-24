import Foundation

/// Never puts the API token in a URL. Redirects are rejected to avoid credential leakage.
final class FilesystemClient: NSObject, URLSessionTaskDelegate {
    let connection: Connection
    private let token: String?
    private let configuration: URLSessionConfiguration
    private lazy var session: URLSession = {
        let config = configuration.copy() as! URLSessionConfiguration
        config.httpCookieStorage = nil
        config.urlCache = nil
        config.timeoutIntervalForRequest = 60
        return URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }()
    private lazy var mediaSession: URLSession = {
        let config = configuration.copy() as! URLSessionConfiguration
        config.httpCookieStorage = nil; config.urlCache = nil
        config.httpMaximumConnectionsPerHost = 8
        config.timeoutIntervalForRequest = 30
        return URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }()
    init(connection: Connection, token: String? = nil, configuration: URLSessionConfiguration = .ephemeral) {
        self.connection = connection; self.token = token; self.configuration = configuration
    }
    func close() { session.invalidateAndCancel(); mediaSession.invalidateAndCancel() }
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }

    static func validateURL(_ value: String) throws -> URL {
        guard var c = URLComponents(string: value.trimmingCharacters(in: .whitespacesAndNewlines)),
              c.scheme?.lowercased() == "https", c.host?.isEmpty == false,
              c.user == nil, c.password == nil, c.query == nil, c.fragment == nil else {
            throw APIError.invalidURL
        }
        c.path = c.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if !c.path.isEmpty { c.path = "/" + c.path }
        guard let url = c.url else { throw APIError.invalidURL }
        return url
    }
    func request(_ route: String, query: [String: String] = [:], method: String = "GET") throws -> URLRequest {
        let base = try Self.validateURL(connection.filesystemURL.absoluteString)
        var c = URLComponents(url: base.appendingPathComponent("v1/fs/" + route), resolvingAgainstBaseURL: false)!
        c.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        // Go query decoding treats an unescaped '+' as a space.
        c.percentEncodedQuery = c.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        var r = URLRequest(url: c.url!)
        r.httpMethod = method
        r.setValue("Bearer " + (try token ?? TokenStore.read(connection.filesystemTokenID)), forHTTPHeaderField: "Authorization")
        return r
    }
    func check(_ response: URLResponse) throws {
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        switch code {
        case 200, 201, 204, 206: return
        case 401: throw APIError.authentication
        case 403: throw APIError.forbidden
        case 404: throw APIError.missing
        case 409, 412: throw APIError.conflict
        default: throw APIError.status(code)
        }
    }
    func data(_ request: URLRequest) async throws -> Data {
        try Task.checkCancellation()
        let (data, response) = try await session.data(for: request)
        try check(response)
        return data
    }
    func data(_ request: URLRequest, limit: Int) async throws -> Data {
        try Task.checkCancellation()
        let (bytes, response) = try await session.bytes(for: request)
        defer { bytes.task.cancel() }
        try check(response)
        guard response.expectedContentLength <= Int64(limit) else { throw CocoaError(.fileReadTooLarge) }
        var result = Data()
        for try await byte in bytes {
            guard result.count < limit else { throw CocoaError(.fileReadTooLarge) }
            result.append(byte)
        }
        try Task.checkCancellation()
        return result
    }
    func scopes() async throws -> [Scope] {
        let d = try await data(request("sources"))
        do {
            let result = try JSONDecoder().decode(User.self, from: d)
            guard result.version == 1 else { throw APIError.incompatible }
            return result.scopes
        }
        catch { throw APIError.incompatible }
    }
    func resource(source: String, path: String) async throws -> Resource {
        try JSONDecoder().decode(Resource.self, from: await data(request("resources", query: [
            "source": source, "path": path
        ])))
    }
    func createFolder(source: String, path: String) async throws {
        _ = try await data(request("folders", query: ["source": source, "path": path], method: "POST"))
    }
    func move(source: String, from: String, to: String) async throws {
        var r = try request("move", method: "POST")
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: ["source": source, "from": from, "to": to])
        _ = try await data(r)
    }
    func thumbnail(source: String, file: RemoteFile) async throws -> Data {
        guard file.archiveRole != "archive" else { throw APIError.forbidden }
        return try await data(request("thumbnail", query: ["source": source, "path": file.path]))
    }
    func streamRequest(source: String, path: String) async throws -> URLRequest {
        try request("content", query: ["source": source, "path": path])
    }

    func streamChunk(_ request: URLRequest, offset: Int64, length: Int) async throws -> MediaChunk {
        guard offset >= 0, length > 0, length <= MediaRange.chunkSize,
              offset <= Int64.max - Int64(length) else { throw URLError(.badURL) }
        try Task.checkCancellation()
        var bounded = request
        bounded.cachePolicy = .reloadIgnoringLocalCacheData
        bounded.setValue("bytes=\(offset)-\(offset + Int64(length) - 1)", forHTTPHeaderField: "Range")
        bounded.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        let (bytes, response) = try await mediaSession.data(for: bounded)
        try check(response)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return try MediaChunk.decode(bytes, response: http, offset: offset, length: length)
    }
    func download(source: String, path: String) async throws -> URL {
        let (url, response) = try await session.download(for: request("content", query: ["source": source, "path": path]))
        do {
            try check(response)
            let destination = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try FileManager.default.moveItem(at: url, to: destination)
            return destination
        } catch {
            try? FileManager.default.removeItem(at: url)
            throw error
        }
    }
    func upload(source: String, path: String, file: URL, replacing: Bool = false, expectedRevision: String? = nil,
                progress: @escaping (Double) -> Void = { _ in }) async throws {
        try Task.checkCancellation()
        var r = try request("content", query: ["source": source, "path": path], method: replacing ? "PUT" : "POST")
        if replacing {
            guard let expectedRevision, !expectedRevision.isEmpty else { throw APIError.conflict }
            r.setValue("\"" + expectedRevision + "\"", forHTTPHeaderField: "If-Match")
        }
        r.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        let delegate = UploadDelegate(progress)
        let (_, response) = try await session.upload(for: r, fromFile: file, delegate: delegate)
        try check(response)
    }
}

private final class UploadDelegate: NSObject, URLSessionTaskDelegate {
    let update: (Double) -> Void
    init(_ update: @escaping (Double) -> Void) { self.update = update }
    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64,
                    totalBytesSent: Int64, totalBytesExpectedToSend: Int64) {
        if totalBytesExpectedToSend > 0 { update(Double(totalBytesSent) / Double(totalBytesExpectedToSend)) }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
