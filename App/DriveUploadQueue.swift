import SwiftUI
import Combine

@MainActor
final class DriveUploadQueue: ObservableObject {
    private static var queues: [UUID: DriveUploadQueue] = [:]
    static func shared(connection: Connection) -> DriveUploadQueue {
        if let queue = queues[connection.id] { return queue }
        let queue = DriveUploadQueue(connection: connection); queues[connection.id] = queue; return queue
    }
    let connection: Connection
    @Published var error: String?
    private var observation: AnyCancellable?
    private init(connection: Connection) {
        self.connection = connection
        observation = UploadActivity.shared.objectWillChange.sink { [weak self] _ in self?.objectWillChange.send() }
    }
    private var current: UploadTransfer? { UploadActivity.shared.transfers(connection: connection.id).first { $0.destination.hasPrefix("drive:") } }
    var name: String { current?.name ?? "" }
    var phase: String { current?.state ?? "" }
    var progress: Double { current?.progress ?? 0 }
    var busy: Bool { current != nil && current?.error == nil }
    var canRetry: Bool { current?.error != nil }
    func recover() { UploadQueue.shared.recover() }
    func enqueue(_ urls: [URL], parent: String, scoped: Bool = true) async {
        do { try await UploadQueue.shared.enqueue(urls, connection: connection, source: "drive", parent: parent, scoped: scoped) }
        catch { self.error = error.localizedDescription }
    }
    func retry() { if let current { UploadQueue.shared.retry(current.id) } }
    func cancel() { if let current { UploadQueue.shared.cancel(current.id) } }
}
