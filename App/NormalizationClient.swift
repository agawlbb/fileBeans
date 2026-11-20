import Foundation
import CryptoKit

struct NormalizationRequest: Codable {
    let source: String
    let path: String
    var relativePath: String? = nil
}

final class NormalizationClient {
    let transport: CompanionClient
    init(connection: Connection) { transport = CompanionClient(connection: connection) }
    init(transport: CompanionClient) { self.transport = transport }
    func close() { transport.close() }
    func submit(_ request: NormalizationRequest) async throws {
        let encoder = JSONEncoder(); encoder.outputFormatting = .sortedKeys
        let body = try encoder.encode(request)
        let identity = Data((transport.connection.id.uuidString + (transport.connection.companionURL?.absoluteString ?? "")).utf8) + body
        let name = SHA256.hash(data: identity).map { String(format: "%02x", $0) }.joined()
        let directory = try AppConfiguration.directory.appendingPathComponent("normalization-submissions", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let pending = directory.appendingPathComponent(name + ".key")
        let key = (try? String(contentsOf: pending, encoding: .utf8)) ?? UUID().uuidString
        try Data(key.utf8).write(to: pending, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        do {
            _ = try await transport.request("normalization/jobs", method: "POST", body: body, key: key)
            try? FileManager.default.removeItem(at: pending)
        } catch {
            let failure = error as NSError
            if failure.domain == "fileBeans.Companion", [400, 403, 404, 409].contains(failure.code) { try? FileManager.default.removeItem(at: pending) }
            throw error
        }
    }
}
