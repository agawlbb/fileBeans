import SwiftUI

struct VaultsView: View {
    let connection: Connection
    @State private var vaults: [ProtectedVault] = []
    @State private var busy = false
    @State private var error: String?
    @State private var initialize: ProtectedVault?
    @ObservedObject private var keySession = VaultKeySession.shared

    var body: some View {
        List {
            VaultKeyControls(session: keySession)
            Section("Protected folders") {
                if vaults.isEmpty { Text("No protected folders").foregroundStyle(.secondary) }
                ForEach(vaults) { vault in
                    HStack {
                        Label(vault.displayName, systemImage: vault.state == "unlocked" ? "lock.open" : "lock")
                        Spacer()
                        if !vault.initialized {
                            Button("Initialize") { initialize = vault }.disabled(!keySession.backupConfirmed)
                        } else {
                            Button(vault.state == "unlocked" || vault.state == "error" ? "Lock" : "Unlock") {
                                perform {
                                    let client = CompanionClient(connection: connection); defer { client.close() }
                                    let locking = vault.state == "unlocked" || vault.state == "error"
                                    let code = locking ? nil : try await keySession.authorizedCode()
                                    try await client.vaultAction(id: vault.id, action: locking ? "lock" : "unlock", code: code)
                                    try await reload()
                                }
                            }.disabled(vault.state != "unlocked" && vault.state != "error" && !keySession.backupConfirmed)
                        }
                    }
                }
                Button("Unlock all") {
                    perform {
                        let code = try await keySession.authorizedCode()
                        let client = CompanionClient(connection: connection); defer { client.close() }
                        for vault in vaults where vault.initialized && vault.state == "locked" {
                            try await client.vaultAction(id: vault.id, action: "unlock", code: code)
                        }
                        try await reload()
                    }
                }.disabled(!keySession.backupConfirmed || !vaults.contains { $0.initialized && $0.state == "locked" })
            }
        }
        .navigationTitle("Protected folders")
        .disabled(busy)
        .overlay { if busy { ProgressView() } }
        .task { perform { try await reload() } }
        .refreshable { do { try await reload() } catch { self.error = error.localizedDescription } }
        .confirmationDialog("Initialize protected folder?", isPresented: Binding(
            get: { initialize != nil }, set: { if !$0 { initialize = nil } }), titleVisibility: .visible) {
            if let vault = initialize {
                Button("Initialize") {
                    initialize = nil
                    perform {
                        let code = try await keySession.authorizedCode()
                        let client = CompanionClient(connection: connection); defer { client.close() }
                        try await client.vaultAction(id: vault.id, action: "initialize", code: code)
                        try await reload()
                    }
                }
            }
        }
        .alert("Protected folders", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
    }
    private func perform(_ operation: @escaping @MainActor () async throws -> Void) {
        busy = true
        Task { @MainActor in
            defer { busy = false }
            do { try await operation() } catch { self.error = error.localizedDescription }
        }
    }
    private func reload() async throws {
        let client = CompanionClient(connection: connection); defer { client.close() }
        vaults = try await client.vaults()
    }
}
