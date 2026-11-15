import Foundation

/// Never puts the API token in a URL. Redirects are rejected to avoid credential leakage.
final class QuantumClient: NSObject, URLSessionTaskDelegate {
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
    init(connection: Connection, token: String? = nil, configuration: URLSessionConfiguration = .ephemeral) {
        self.connection = connection; self.token = token; self.configuration = configuration
    }
    func close() { session.invalidateAndCancel() }
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
        let base = try Self.validateURL(connection.url.absoluteString)
        var c = URLComponents(url: base.appendingPathComponent("api/" + route), resolvingAgainstBaseURL: false)!
        c.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        // Quantum uses Go's form-style query decoder, where an unescaped '+' is a space.
        c.percentEncodedQuery = c.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        var r = URLRequest(url: c.url!)
        r.httpMethod = method
        r.setValue("Bearer " + (try token ?? TokenStore.read(connection.id)), forHTTPHeaderField: "Authorization")
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
    func scopes() async throws -> [Scope] {
        let d = try await data(request("users", query: ["username": "self"]))
        do { return try JSONDecoder().decode(User.self, from: d).scopes }
        catch { throw APIError.incompatible }
    }
    func resource(source: String, path: String) async throws -> Resource {
        try JSONDecoder().decode(Resource.self, from: await data(request("resources", query: [
            "source": source, "path": path
        ])))
    }
    func createFolder(source: String, path: String) async throws {
        _ = try await data(request("resources", query: ["source": source, "path": path, "isDir": "true", "override": "false"], method: "POST"))
    }
    func delete(source: String, path: String) async throws {
        _ = try await data(request("resources", query: ["source": source, "path": path], method: "DELETE"))
    }
    func move(source: String, from: String, to: String) async throws {
        var r = try request("resources", method: "PATCH")
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: [
            "action": "move", "overwrite": false, "rename": false,
            "items": [["fromSource": source, "fromPath": from, "toSource": source, "toPath": to]]
        ])
        _ = try await data(r)
    }
    func thumbnail(source: String, file: RemoteFile) async throws -> Data {
        try await data(request("resources/preview", query: ["source": source, "path": file.path,
            "size": "small", "key": file.modified ?? ""]))
    }
    func streamRequest(source: String, path: String) async throws -> URLRequest {
        let file = try await resource(source: source, path: path)
        guard let viewToken = file.viewToken, !viewToken.isEmpty else { throw APIError.forbidden }
        // Quantum requires both a view grant and user authentication on this route.
        return try request("media/stream", query: ["source": source, "file": path, "viewToken": viewToken])
    }
    func streamChunk(_ request: URLRequest, offset: Int64, length: Int) async throws -> MediaChunk {
        guard offset >= 0, length > 0, length <= MediaRange.chunkSize,
              offset <= Int64.max - Int64(length) else { throw URLError(.badURL) }
        try Task.checkCancellation()
        var bounded = request
        bounded.cachePolicy = .reloadIgnoringLocalCacheData
        bounded.setValue("bytes=\(offset)-\(offset + Int64(length) - 1)", forHTTPHeaderField: "Range")
        bounded.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        let (bytes, response) = try await session.data(for: bounded)
        try check(response)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return try MediaChunk.decode(bytes, response: http, offset: offset, length: length)
    }
    func download(source: String, path: String) async throws -> URL {
        let (url, response) = try await session.download(for: request("resources/download", query: ["source": source, "file": path]))
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
    func upload(source: String, path: String, file: URL, replacing: Bool = false,
                progress: @escaping (Double) -> Void = { _ in }) async throws {
        try Task.checkCancellation()
        var r = try request("resources", query: ["source": source, "path": path, "override": "false"], method: replacing ? "PUT" : "POST")
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
