import SwiftUI
import UIKit

@MainActor
final class VaultKeySession: ObservableObject {
    static let shared = VaultKeySession()
    private static let pendingBackup = "vault.pendingBackup.v1"
    @Published var code = ""
    @Published var importCode = ""
    @Published private(set) var hasKey: Bool
    @Published var backupConfirmed = false
    @Published var copied = false
    @Published var busy = false
    @Published var error: String?
    private let defaults: UserDefaults
    private let saveKey: (String) async throws -> Void
    private let readKey: () async throws -> String
    private let removeKey: () async throws -> Void

    init(defaults: UserDefaults = .standard,
         containsKey: () -> Bool = VaultKeyStore.containsKey,
         saveKey: @escaping (String) async throws -> Void = VaultKeyStore.save,
         readKey: @escaping () async throws -> String = VaultKeyStore.read,
         removeKey: @escaping () async throws -> Void = VaultKeyStore.remove) {
        self.defaults = defaults; self.saveKey = saveKey; self.readKey = readKey; self.removeKey = removeKey
        hasKey = containsKey()
        backupConfirmed = hasKey && !defaults.bool(forKey: Self.pendingBackup)
    }

    func generate() async {
        await store(imported: false) { try VaultRecoveryCode.generate() }
    }
    func importKey() async { await store(imported: true) { self.importCode } }
    private func store(imported: Bool, _ make: () throws -> String) async {
        busy = true; defer { busy = false }
        do {
            guard !hasKey else { throw VaultRecoveryCode.failure("A key is already stored.") }
            let value = try VaultRecoveryCode.encode(VaultRecoveryCode.decode(make()))
            defaults.set(true, forKey: Self.pendingBackup)
            try await saveKey(value)
            if imported { defaults.set(false, forKey: Self.pendingBackup) }
            hasKey = true; code = try VaultRecoveryCode.encode(VaultRecoveryCode.decode(value))
            importCode = ""; backupConfirmed = imported; copied = false
        } catch { self.error = error.localizedDescription }
    }
    func reveal() async {
        busy = true; defer { busy = false }
        do { code = try await readKey(); copied = false }
        catch { self.error = error.localizedDescription }
    }
    func confirmBackup() {
        defaults.set(false, forKey: Self.pendingBackup)
        backupConfirmed = true
    }
    func copy() {
        guard !code.isEmpty else { return }
        UIPasteboard.general.setItems([[UIPasteboard.typeAutomatic: code]], options: [:])
        copied = true
    }
    func hide() { code = ""; copied = false }
    func forget() async {
        busy = true; defer { busy = false }
        do {
            try await removeKey()
            hide(); importCode = ""; hasKey = false; backupConfirmed = false
            defaults.removeObject(forKey: Self.pendingBackup)
        } catch { self.error = error.localizedDescription }
    }
    func authorizedCode(requireBackup: Bool = true) async throws -> String {
        guard hasKey, (!requireBackup || backupConfirmed) else { throw VaultRecoveryCode.failure("Save your recovery code first.") }
        return try await readKey()
    }
}

struct VaultKeyControls: View {
    @ObservedObject var session = VaultKeySession.shared
    @State private var forget = false
    @Environment(\.scenePhase) private var scenePhase
    var body: some View {
        Section("Encryption key") {
            if session.hasKey {
                Button("Show recovery code") { Task { await session.reveal() } }
                Button("Forget key", role: .destructive) { forget = true }
            } else {
                Button("Generate key") { Task { await session.generate() } }
                TextField("Recovery code", text: $session.importCode, axis: .vertical)
                    .textInputAutocapitalization(.never).autocorrectionDisabled().privacySensitive()
                PasteButton(payloadType: String.self) { values in
                    if let value = values.first { session.importCode = value }
                }
                Button("Import key") { Task { await session.importKey() } }.disabled(session.importCode.isEmpty)
            }
            if !session.code.isEmpty {
                Text(session.code).font(.caption.monospaced()).textSelection(.enabled).privacySensitive()
                Button(session.copied ? "Copied" : "Copy to Clipboard", systemImage: "doc.on.doc") { session.copy() }
                Button("Hide") { session.hide() }
            }
            if session.hasKey && !session.backupConfirmed {
                Toggle("Recovery code saved", isOn: Binding(get: { session.backupConfirmed }, set: { if $0 { session.confirmBackup() } }))
            }
        }
        .disabled(session.busy)
        .overlay { if scenePhase != .active { Color(.systemBackground) } }
        .confirmationDialog("Forget key?", isPresented: $forget, titleVisibility: .visible) {
            Button("Forget key", role: .destructive) { Task { await session.forget() } }
        }
        .alert("Encryption key", isPresented: Binding(get: { session.error != nil }, set: { if !$0 { session.error = nil } })) {
            Button("OK") { session.error = nil }
        } message: { Text(session.error ?? "") }
    }
}
