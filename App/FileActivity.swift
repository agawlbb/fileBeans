import Foundation
import SwiftUI

struct ActivityFile {
    let source: String
    let path: String
    let name: String
    let folder: Bool
}
struct FileActivityEntry: Identifiable {
    let id: UUID
    let connection: UUID
    let file: ActivityFile
    let destination: CopyLocation?
    let operation: String
    var job: String?
}
@MainActor
final class FileActivity: ObservableObject {
    static let shared = FileActivity()
    @Published private(set) var entries: [FileActivityEntry] = []
    @Published private(set) var jobs: [UUID: [CopyJob]] = [:]
    @Published var errors: [UUID: String] = [:]

    func begin(_ files: [ActivityFile], connection: UUID, operation: String, destination: CopyLocation? = nil) -> [UUID] {
        let pending = files.map { FileActivityEntry(id: UUID(), connection: connection, file: $0, destination: destination, operation: operation) }
        entries += pending
        return pending.map(\.id)
    }
    func finish(_ ids: [UUID]) { entries.removeAll { ids.contains($0.id) } }
    func attach(_ job: CopyJob, ids: [UUID], connection: UUID) {
        for i in entries.indices where ids.contains(entries[i].id) { entries[i].job = job.id }
        var known = jobs[connection] ?? []; known.removeAll { $0.id == job.id }; known.append(job)
        jobs[connection] = known
    }
    func synchronize(_ incoming: [CopyJob], connection: UUID) {
        let active = Set(incoming.filter(\.active).map(\.id))
        let previous = Set((jobs[connection] ?? []).map(\.id))
        if let failed = incoming.first(where: { $0.state == "failed" && previous.contains($0.id) }) {
            errors[connection] = failed.title + ": " + (failed.error ?? "Transfer failed")
        }
        entries.removeAll { $0.connection == connection && $0.job.map { !active.contains($0) } == true }
        jobs[connection] = incoming.filter(\.active)
    }
    func deleting(connection: UUID, source: String, path: String) -> Bool {
        entries.contains { $0.connection == connection && $0.operation == "delete" && $0.file.source == source && $0.file.path == path }
    }

    func busySource(connection: UUID, source: String, path: String, canonicalPath: String) -> Bool {
        if entries.contains(where: { $0.connection == connection && $0.operation != "copy" && $0.file.source == source && $0.file.path == path }) { return true }
        return (jobs[connection] ?? []).contains { job in
            job.active && job.request?.operation == "move" && job.request?.items.contains {
                $0.source == source && FilePath.normalized($0.path) == FilePath.normalized(canonicalPath)
            } == true
        }
    }
    func pending(connection: UUID, source: String, parent: String, canonicalParent: String) -> [ActivityFile] {
        func location(_ value: String) -> String { source == "drive" ? value : FilePath.normalized(value == "." ? "" : value) }
        var result = entries.filter { $0.connection == connection && $0.destination?.source == source &&
            $0.destination.map { location($0.path) == location(parent) } == true && $0.operation != "delete" }.map {
                ActivityFile(source: source, path: source == "drive" ? "pending:" + $0.id.uuidString : FilePath.normalized(parent + "/" + $0.file.name),
                             name: $0.file.name, folder: $0.file.folder)
            }
        for job in jobs[connection] ?? [] where job.active {
            guard let request = job.request, request.destination.source == source,
                  location(request.destination.path) == location(canonicalParent) else { continue }
            for item in request.items {
                let name = request.destinationName ?? item.name ?? (item.path as NSString).lastPathComponent
                if !result.contains(where: { $0.name == name }) {
                    result.append(ActivityFile(source: source, path: source == "drive" ? "pending:" + job.id + ":" + name : FilePath.normalized(parent + "/" + name), name: name, folder: false))
                }
            }
        }
        return result
    }
}
