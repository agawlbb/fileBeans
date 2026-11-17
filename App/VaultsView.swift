import SwiftUI
import UIKit

struct ProtectedVault: Decodable, Identifiable {
    let id: String
    let source: String
    let state: String
    let initialized: Bool
}

struct VaultsView: View {
    let connection: Connection
    @State private var vaults: [ProtectedVault] = []
    @State private var hasKey = VaultKeyStore.containsKey()
    @State private var code = ""
    @State private var savedBackup = false
    @State private var busy = false
    @State private var error: String?
    @State private var initialize: ProtectedVault?
    @State private var forgetKey = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        List {
            Section("Encryption key") {
                if hasKey {
                    Label("Key stored on this device", systemImage: "faceid")
                    Button("Show recovery code") { perform { code = try await VaultKeyStore.read() } }
                    Button("Forget key on this device", role: .destructive) { forgetKey = true }
                } else {
                    Button("Generate master key") { do { code = try VaultRecoveryCode.generate() } catch { self.error = error.localizedDescription } }
                    TextField("Paste recovery code", text: $code, axis: .vertical)
                        .textInputAutocapitalization(.never).autocorrectionDisabled().privacySensitive()
                    Toggle("I have stored a secure backup of this code", isOn: $savedBackup)
                    Button("Store key with biometrics") {
                        perform { try await VaultKeyStore.save(code); code = ""; hasKey = true }
                    }.disabled(code.isEmpty || !savedBackup)
                }
                if !code.isEmpty {
                    Text(code).font(.caption.monospaced()).privacySensitive()
                    Button("Copy recovery code") {
                        UIPasteboard.general.setItems([[UIPasteboard.typeAutomatic: code]],
                            options: [.localOnly: true, .expirationDate: Date().addingTimeInterval(60)])
                    }
                    Button("Hide code") { code = "" }
                    Text("This code contains the actual encryption key. Anyone with it can decrypt your vaults. Keep the complete encrypted folders, including hidden metadata, for independent gocryptfs recovery.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            Section("Protected folders") {
                if vaults.isEmpty { Text("No protected folders configured on this companion.").foregroundStyle(.secondary) }
                ForEach(vaults) { vault in
                    VStack(alignment: .leading, spacing: 8) {
                        Label(vault.source, systemImage: vault.state == "unlocked" ? "lock.open" : "lock")
                        Text(vault.state.capitalized).font(.caption).foregroundStyle(.secondary)
                        if vault.state == "unlocked" || vault.state == "error" {
                            Button("Lock") { perform { try await action(vault, "lock") } }
                        }
                        if vault.state == "locked" && vault.initialized {
                            Button("Unlock") { perform { try await action(vault, "unlock", code: VaultKeyStore.read()) } }.disabled(!hasKey)
                        }
                        if !vault.initialized {
                            Button("Initialize empty vault") { initialize = vault }.disabled(!hasKey)
                        }
                    }
                }
                Button("Unlock all initialized folders") {
                    perform {
                        let key = try await VaultKeyStore.read()
                        for vault in vaults where vault.initialized && vault.state == "locked" { try await action(vault, "unlock", code: key) }
                    }
                }.disabled(!hasKey || !vaults.contains { $0.initialized && $0.state == "locked" })
            }
            Section {
                Text("Unlocked folders stay accessible through the server until you lock them or restart the server. Direct access to the original Samba share shows encrypted files. Mobile caches are unchanged.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .navigationTitle("Protected folders")
        .disabled(busy)
        .overlay { if busy { ProgressView() } }
        .task { perform { try await reload() } }
        .refreshable { do { try await reload() } catch { self.error = error.localizedDescription } }
        .onDisappear { code = "" }
        .onChange(of: scenePhase) { _, phase in if phase != .active { code = "" } }
        .confirmationDialog("Remove this device’s key?", isPresented: $forgetKey, titleVisibility: .visible) {
            Button("Forget key", role: .destructive) {
                perform { try await VaultKeyStore.remove(); code = ""; hasKey = false; savedBackup = false }
            }
        } message: { Text("You will need your saved recovery code to use protected folders again. Server vaults and other devices are unchanged.") }
        .alert("Protected folders", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
        .confirmationDialog("Initialize this empty vault with your saved master key?", isPresented: Binding(
            get: { initialize != nil }, set: { if !$0 { initialize = nil } }), titleVisibility: .visible) {
                if let vault = initialize {
                    Button("Initialize vault") { perform { try await action(vault, "initialize", code: VaultKeyStore.read()) }; initialize = nil }
                }
            } message: { Text("The server must already have an empty encrypted-storage directory configured. Existing folders are not converted or deleted.") }
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
        vaults = try JSONDecoder().decode([ProtectedVault].self, from: await client.request("vaults"))
    }
    private func action(_ vault: ProtectedVault, _ action: String, code: String? = nil) async throws {
        let client = CompanionClient(connection: connection); defer { client.close() }
        let body = try code.map { try JSONEncoder().encode(["key": $0]) }
        _ = try await client.request("vaults/\(vault.id)/\(action)", method: "POST", body: body, permanent: action == "initialize")
        try await reload()
    }
}
