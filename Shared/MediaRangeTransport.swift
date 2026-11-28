import Foundation

/// A dedicated authenticated media session delivering URLSession's data callbacks,
/// rather than waiting for a whole range response to finish.
final class MediaRangeTransport: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    private final class Read {
        let offset: Int64
        let length: Int
        let continuation: AsyncThrowingStream<MediaChunk, Error>.Continuation
        var response: MediaRangeResponse?
        var received = 0
        init(offset: Int64, length: Int, continuation: AsyncThrowingStream<MediaChunk, Error>.Continuation) {
            self.offset = offset; self.length = length; self.continuation = continuation
        }
    }
    private let connection: Connection?
    private let configuration: URLSessionConfiguration
    private lazy var session = URLSession(configuration: configuration, delegate: self, delegateQueue: nil)
    private let lock = NSLock()
    private var reads: [Int: Read] = [:]

    init(configuration: URLSessionConfiguration, connection: Connection? = nil) {
        self.connection = connection
        let config = configuration.copy() as! URLSessionConfiguration
        config.httpCookieStorage = nil; config.urlCache = nil
        config.httpMaximumConnectionsPerHost = 8
        config.timeoutIntervalForRequest = 30
        self.configuration = config
        super.init()
    }

    func chunks(_ request: URLRequest, offset: Int64, length: Int) -> AsyncThrowingStream<MediaChunk, Error> {
        AsyncThrowingStream { continuation in
            let operation = Task {
                do {
                    let routed: URLRequest
                    if let connection { routed = try await ServerRoute.shared.prepare(request, connection: connection) }
                    else { routed = request }
                    for try await value in rawChunks(routed, offset: offset, length: length) { continuation.yield(value) }
                    continuation.finish()
                } catch {
                    if let connection, ServerRoute.connectionFailed(error) { await ServerRoute.shared.preferRemote(connection) }
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in operation.cancel() }
        }
    }
    private func rawChunks(_ request: URLRequest, offset: Int64, length: Int) -> AsyncThrowingStream<MediaChunk, Error> {
        AsyncThrowingStream { continuation in
            guard offset >= 0, length > 0, length <= MediaRange.chunkSize,
                  offset <= Int64.max - Int64(length) else {
                continuation.finish(throwing: URLError(.badURL)); return
            }
            var bounded = request
            bounded.cachePolicy = .reloadIgnoringLocalCacheData
            bounded.setValue("bytes=\(offset)-\(offset + Int64(length) - 1)", forHTTPHeaderField: "Range")
            bounded.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
            let task = session.dataTask(with: bounded)
            let read = Read(offset: offset, length: length, continuation: continuation)
            lock.withLock { reads[task.taskIdentifier] = read }
            continuation.onTermination = { [weak self] _ in
                if let self { _ = self.lock.withLock { self.reads.removeValue(forKey: task.taskIdentifier) } }
                task.cancel()
            }
            task.resume()
        }
    }

    func close() { session.invalidateAndCancel() }

    func chunk(_ request: URLRequest, offset: Int64, length: Int) async throws -> MediaChunk {
        try Task.checkCancellation()
        var bytes = Data()
        var metadata: MediaChunk?
        for try await chunk in chunks(request, offset: offset, length: length) {
            try Task.checkCancellation()
            bytes.append(chunk.data); metadata = chunk
        }
        try Task.checkCancellation()
        guard let metadata else { throw URLError(.badServerResponse) }
        return MediaChunk(data: bytes, totalLength: metadata.totalLength, mimeType: metadata.mimeType)
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard let connection else { completionHandler(.performDefaultHandling, nil); return }
        ServerTrust.handle(challenge, connection: connection, completion: completionHandler)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        guard let connection, connection.isGoogleDrive, let url = request.url,
              GoogleDriveTransport.googleURL(url) else { completionHandler(nil); return }
        Task {
            do {
                var next = request
                next.setValue("Bearer " + (try await GoogleDriveAuthorization.shared.accessToken(connection)), forHTTPHeaderField: "Authorization")
                if let range = task.originalRequest?.value(forHTTPHeaderField: "Range") { next.setValue(range, forHTTPHeaderField: "Range") }
                completionHandler(next)
            } catch { completionHandler(nil) }
        }
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        guard let read = lock.withLock({ reads[dataTask.taskIdentifier] }) else { completionHandler(.cancel); return }
        do {
            guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
            read.response = try MediaRangeResponse(http, offset: read.offset, length: read.length)
            completionHandler(.allow)
        } catch {
            _ = lock.withLock { reads.removeValue(forKey: dataTask.taskIdentifier) }
            read.continuation.finish(throwing: error)
            completionHandler(.cancel)
        }
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard let read = lock.withLock({ reads[dataTask.taskIdentifier] }), let response = read.response else { return }
        guard data.count <= response.length - read.received else {
            _ = lock.withLock { reads.removeValue(forKey: dataTask.taskIdentifier) }
            read.continuation.finish(throwing: URLError(.badServerResponse)); dataTask.cancel(); return
        }
        read.received += data.count
        if !data.isEmpty {
            read.continuation.yield(MediaChunk(data: data, totalLength: response.total, mimeType: response.mimeType))
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard let read = lock.withLock({ reads.removeValue(forKey: task.taskIdentifier) }) else { return }
        if let error { read.continuation.finish(throwing: error) }
        else if read.response?.length != read.received { read.continuation.finish(throwing: URLError(.badServerResponse)) }
        else { read.continuation.finish() }
    }
}
