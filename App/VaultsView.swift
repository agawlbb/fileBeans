import SwiftUI

struct ProtectionMemberStatus: Decodable, Equatable, Identifiable {
    let server: String
    let name: String
    let state: String
    let error: String?
    var id: String { server }
}
struct ProtectionStatus: Decodable, Equatable {
    let state: String
    var group: String? = nil
    var groupState: String? = nil
    var members: [ProtectionMemberStatus]? = nil
    var hasUnlocked: Bool { members?.contains { $0.state == "unlocked" || $0.state == "error" } ?? (state == "unlocked" || state == "error") }
    var needsUnlock: Bool { !hasUnlocked || groupState == "partial" }
}
struct ProtectionControlGroup: Identifiable {
    let id: String
    let connections: [Connection]
    let status: ProtectionStatus?
    static func groups(_ connections: [Connection], statuses: [UUID: ProtectionStatus], reachable: Set<UUID>) -> [Self] {
        var order: [String] = []
        var buckets: [String: [Connection]] = [:]
        for connection in connections where !connection.isGoogleDrive {
            let known = statuses[connection.id]?.group ?? statuses.values.first { status in
                status.members?.contains { $0.server == connection.serverID } == true
            }?.group
            let id = known ?? connection.id.uuidString
            if buckets[id] == nil { order.append(id) }
            buckets[id, default: []].append(connection)
        }
        return order.map { id in
            let items = buckets[id]!
            let chosen = items.first { reachable.contains($0.id) } ?? items[0]
            return Self(id: id, connections: [chosen] + items.filter { $0.id != chosen.id }, status: reachable.contains(chosen.id) ? statuses[chosen.id] : nil)
        }
    }
}

@MainActor
enum ProtectionChanges {
    static func apply(_ status: ProtectionStatus, origin: Connection) async throws {
        let store = try SharedStore()
        let configured = try store.connections().filter { !$0.isGoogleDrive }
        for connection in configured {
            let member = status.members?.first { $0.server == connection.serverID }
            guard member != nil || connection.id == origin.id else { continue }
            let state = member?.state ?? status.state
            if state == "locked" {
                try store.markProtectionLocked(connection: connection.id)
                await ThumbnailCache.shared.clear(connection: connection.id)
            } else if state == "unlocked" {
                let client = CompanionClient(connection: connection); defer { client.close() }
                if let vaults = try? await client.vaults() { try store.setVaults(vaults, connection: connection.id) }
            }
            try Repository(connection: connection, store: store).notify()
        }
        NotificationCenter.default.post(name: .fileBeansProtectionChanged, object: nil)
    }
}

struct VaultsView: View {
    let connections: [Connection]
    var embedded = false
    init(connection: Connection, embedded: Bool = false) { self.connections = [connection]; self.embedded = embedded }
    init(connections: [Connection], embedded: Bool = false) { self.connections = connections; self.embedded = embedded }
    @State private var statuses: [UUID: ProtectionStatus] = [:]
    @State private var reachable: Set<UUID> = []
    @State private var busy = false
    @State private var revision = 0
    @State private var error: String?
    @ObservedObject private var keySession = VaultKeySession.shared
    private var groups: [ProtectionControlGroup] { ProtectionControlGroup.groups(connections, statuses: statuses, reachable: reachable) }
    var body: some View {
        Group {
            if embedded { controls } else { List { controls } }
        }
        .modifier(ProtectionLifecycle(refresh: refresh))
        .alert("Protected folders", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
    }
    private var controls: some View {
        Section("Protected folders") {
            ForEach(groups) { group in
                if groups.count > 1 { Text(group.connections[0].name) }
                if let status = group.status {
                    if status.hasUnlocked { Button("Lock") { changeProtection(group, action: "lock") }.disabled(busy) }
                    if status.needsUnlock { Button("Unlock") { changeProtection(group, action: "unlock") }.disabled(busy || !keySession.hasKey) }
                    if status.groupState == "partial", let members = status.members {
                        ForEach(members) { member in
                            LabeledContent(member.name, value: member.error ?? member.state.capitalized)
                        }
                    }
                } else { Text("Unavailable").foregroundStyle(.secondary) }
            }
            if busy { ProgressView() }
        }
    }
    private func refresh() async {
        let requestedRevision = revision
        for connection in connections.sorted(by: { $0.localURL != nil && $1.localURL == nil }) {
            guard !Task.isCancelled, !busy, requestedRevision == revision else { return }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do {
                let value = try JSONDecoder().decode(ProtectionStatus.self, from: await client.request("protection"))
                guard !Task.isCancelled, !busy, requestedRevision == revision else { return }
                let changed = statuses[connection.id] != value
                statuses[connection.id] = value; reachable.insert(connection.id)
                if changed { try await ProtectionChanges.apply(value, origin: connection) }
            } catch { reachable.remove(connection.id) }
        }
    }
    private func changeProtection(_ group: ProtectionControlGroup, action: String) {
        guard !busy, let connection = group.connections.first else { return }
        busy = true; revision += 1
        Task { @MainActor in
            defer { busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do {
                let code = action == "unlock" ? try await keySession.authorizedCode(requireBackup: false) : nil
                let body = try code.map { try JSONEncoder().encode(["key": $0]) }
                let status = try JSONDecoder().decode(ProtectionStatus.self,
                    from: await client.request("protection/" + action, method: "POST", body: body,
                                               key: UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()))
                for c in group.connections { statuses[c.id] = status }
                try await ProtectionChanges.apply(status, origin: connection)
                error = nil
            } catch { self.error = error.localizedDescription }
        }
    }
}

extension Notification.Name {
    static let fileBeansProtectionChanged = Notification.Name("fileBeans.protectionChanged")
}
private struct ProtectionLifecycle: ViewModifier {
    let refresh: () async -> Void
    func body(content: Content) -> some View {
        content.task {
            while !Task.isCancelled {
                await refresh()
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
            }
        }
    }
}
