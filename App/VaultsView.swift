import SwiftUI

struct ProtectionStatus: Decodable, Equatable {
    let state: String
}

struct VaultsView: View {
    let connection: Connection
    var embedded = false
    @State private var status: ProtectionStatus?
    @State private var busy = false
    @State private var revision = 0
    @State private var error: String?
    @ObservedObject private var keySession = VaultKeySession.shared

    private var locking: Bool { status?.state == "unlocked" || status?.state == "error" }
    var body: some View {
        Group {
            if embedded { controls }
            else { List { controls } }
        }
        .modifier(ProtectionLifecycle(refresh: refresh))
        .alert("Protected folders", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
    }
    private var controls: some View {
            Section(embedded ? connection.name : "Protected folders") {
                if let status {
                    Button(locking ? "Lock" : "Unlock") { changeProtection() }
                        .disabled(busy || (!locking && !keySession.hasKey))
                    if busy { ProgressView() }
                    if status.state == "error" { Text("Protection needs attention").foregroundStyle(.red) }
                } else if let error { Text(error).foregroundStyle(.red) }
                else { ProgressView() }
            }
    }
    private func refresh() async {
        let requestedRevision = revision
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            let value = try JSONDecoder().decode(ProtectionStatus.self, from: await client.request("protection"))
            guard !Task.isCancelled, !busy, requestedRevision == revision else { return }
            status = value; error = nil
        } catch {
            guard !Task.isCancelled, !busy, requestedRevision == revision else { return }
            self.error = error.localizedDescription
        }
    }
    private func changeProtection() {
        guard !busy else { return }
        let action = locking ? "lock" : "unlock"
        busy = true
        revision += 1
        Task { @MainActor in
            defer { busy = false }
            let client = CompanionClient(connection: connection); defer { client.close() }
            do {
                let code = action == "unlock" ? try await keySession.authorizedCode(requireBackup: false) : nil
                let body = try code.map { try JSONEncoder().encode(["key": $0]) }
                status = try JSONDecoder().decode(ProtectionStatus.self,
                    from: await client.request("protection/" + action, method: "POST", body: body))
                let store = try SharedStore()
                if status?.state == "locked" { try store.markProtectionLocked(connection: connection.id) }
                try Repository(connection: connection, store: store).notify()
                NotificationCenter.default.post(name: .fileBeansProtectionChanged, object: connection.id)
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
