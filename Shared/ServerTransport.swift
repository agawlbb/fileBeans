import Foundation
import CryptoKit
import Security
import Network

enum ServerTrust {
    static func handle(_ challenge: URLAuthenticationChallenge, connection: Connection,
                       completion: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard let local = connection.localURL,
              challenge.protectionSpace.host.caseInsensitiveCompare(local.host ?? "") == .orderedSame,
              challenge.protectionSpace.port == (local.port ?? 443) else {
            completion(.performDefaultHandling, nil); return
        }
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate], let leaf = chain.first,
              let pin = connection.localFingerprint else { completion(.cancelAuthenticationChallenge, nil); return }
        let digest = SHA256.hash(data: SecCertificateCopyData(leaf) as Data).map { String(format: "%02x", $0) }.joined()
        guard digest == pin.lowercased() else { completion(.cancelAuthenticationChallenge, nil); return }
        // The verified fingerprint authenticates this server; basic X.509 still
        // checks certificate validity without requiring DNS for a private IP.
        SecTrustSetPolicies(trust, SecPolicyCreateBasicX509())
        SecTrustSetAnchorCertificates(trust, [leaf] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        guard SecTrustEvaluateWithError(trust, nil) else { completion(.cancelAuthenticationChallenge, nil); return }
        completion(.useCredential, URLCredential(trust: trust))
    }
}

private final class ProbeDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    let connection: Connection
    private let lock = NSLock()
    private var trustRejected = false
    var rejected: Bool { lock.withLock { trustRejected } }
    init(_ connection: Connection) { self.connection = connection }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        ServerTrust.handle(challenge, connection: connection) { disposition, credential in
            if disposition == .cancelAuthenticationChallenge { self.lock.withLock { self.trustRejected = true } }
            completionHandler(disposition, credential)
        }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}

actor ServerRoute {
    static let shared = ServerRoute()
    private struct Entry { let local: Bool; let expires: Date }
    private var cache: [Connection: Entry] = [:]
    private let monitor = NWPathMonitor()
    init() {
        monitor.pathUpdateHandler = { _ in Task { await ServerRoute.shared.clear() } }
        monitor.start(queue: DispatchQueue(label: "fileBeans.network-route"))
    }
    private func clear() { cache.removeAll() }

    func prepare(_ request: URLRequest, connection: Connection) async throws -> URLRequest {
        guard let local = connection.localURL, connection.localFingerprint != nil,
              let identity = connection.serverID, !identity.isEmpty else { return request }
        let useLocal: Bool
        if let entry = cache[connection], entry.expires > Date() { useLocal = entry.local }
        else {
            let configuration = URLSessionConfiguration.ephemeral
            configuration.timeoutIntervalForRequest = 1.5
            configuration.timeoutIntervalForResource = 2
            configuration.httpCookieStorage = nil; configuration.urlCache = nil
            let delegate = ProbeDelegate(connection)
            let session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
            defer { session.invalidateAndCancel() }
            var probe = URLRequest(url: local.appendingPathComponent("v1/capabilities"))
            probe.setValue(request.value(forHTTPHeaderField: "Authorization"), forHTTPHeaderField: "Authorization")
            do {
                let (data, response) = try await session.data(for: probe)
                struct Identity: Decodable { let serverId: String }
                guard (response as? HTTPURLResponse)?.statusCode == 200,
                      try JSONDecoder().decode(Identity.self, from: data).serverId == identity else {
                    throw URLError(.serverCertificateUntrusted)
                }
                useLocal = true
            } catch {
                try Task.checkCancellation()
                if delegate.rejected { throw URLError(.serverCertificateUntrusted) }
                // Certificate/identity failures must be visible, not disguised as an outage.
                if let error = error as? URLError,
                   [.serverCertificateUntrusted, .serverCertificateHasBadDate, .secureConnectionFailed,
                    .serverCertificateHasUnknownRoot, .serverCertificateNotYetValid].contains(error.code) { throw error }
                useLocal = false
            }
            cache[connection] = Entry(local: useLocal, expires: Date().addingTimeInterval(10))
        }
        guard useLocal else { return request }
        return try Self.rebased(request, from: connection.filesystemURL, to: local)
    }
    nonisolated static func rebased(_ request: URLRequest, from remote: URL, to local: URL) throws -> URLRequest {
        guard let original = request.url else { throw URLError(.badURL) }
        let base = try FilesystemClient.validateURL(remote.absoluteString)
        let target = try FilesystemClient.validateURL(local.absoluteString)
        guard original.scheme == base.scheme, original.host == base.host, original.port == base.port,
              original.path.hasPrefix(base.path.isEmpty ? "/" : base.path + "/") else { throw URLError(.badURL) }
        let suffix = String(original.path.dropFirst(base.path.count))
        var parts = URLComponents(url: target, resolvingAgainstBaseURL: false)!
        parts.path = target.path + suffix
        parts.percentEncodedQuery = URLComponents(url: original, resolvingAgainstBaseURL: false)?.percentEncodedQuery
        guard let url = parts.url else { throw URLError(.badURL) }
        var routed = request; routed.url = url; return routed
    }
    func invalidate(_ connection: Connection) { cache.removeValue(forKey: connection) }
    func preferRemote(_ connection: Connection) { cache[connection] = Entry(local: false, expires: Date().addingTimeInterval(10)) }
    nonisolated static func connectionFailed(_ error: Error) -> Bool {
        guard let error = error as? URLError else { return false }
        return [.cannotConnectToHost, .cannotFindHost, .networkConnectionLost, .notConnectedToInternet,
                .timedOut, .dnsLookupFailed].contains(error.code)
    }
    func data(_ request: URLRequest, connection: Connection, session: URLSession) async throws -> (Data, URLResponse) {
        let routed = try await prepare(request, connection: connection)
        do { return try await session.data(for: routed) }
        catch {
            guard Self.connectionFailed(error), routed.url != request.url else { throw error }
            preferRemote(connection)
            guard ["GET", "HEAD"].contains(request.httpMethod ?? "GET") else { throw error }
            try Task.checkCancellation()
            return try await session.data(for: request)
        }
    }
}
