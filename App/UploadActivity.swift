import SwiftUI

struct UploadTransfer: Identifiable {
    let id: UUID
    let connection: UUID
    let name: String
    let destination: String
    let total: Int64
    var progress: Double = 0
    var state = "Queued"
    var error: String?
    var job: String?
}

@MainActor
final class UploadActivity: ObservableObject {
    static let shared = UploadActivity()
    @Published private(set) var entries: [UploadTransfer] = []

    func add(_ entry: UploadTransfer) {
        guard !entries.contains(where: { $0.id == entry.id }) else { return }
        entries.append(entry)
    }
    func update(_ id: UUID, progress: Double? = nil, state: String? = nil, error: String? = nil, job: String? = nil) {
        guard let index = entries.firstIndex(where: { $0.id == id }) else { return }
        if let progress { entries[index].progress = min(1, max(0, progress)) }
        if let state { entries[index].state = state }
        entries[index].error = error
        if let job { entries[index].job = job }
    }
    func remove(_ id: UUID) { entries.removeAll { $0.id == id } }
    func transfers(connection: UUID) -> [UploadTransfer] { entries.filter { $0.connection == connection } }
}
