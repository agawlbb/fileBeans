import SwiftUI

struct DriveConnectionSettings: View {
    let connection: Connection
    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var phase
    @State private var connected = false
    @State private var writable = false
    @State private var busy = false
    @State private var loaded = false
    @State private var disconnecting = false
    @State private var error: String?

    var body: some View {
        List {
            if loaded {
                Button(writable ? "Reconnect Drive" : connected ? "Upgrade Drive Access" : "Connect Drive") {
                    Task {
                        busy = true; defer { busy = false }
                        let client = DriveClient(connection: connection); defer { client.close() }
                        do { openURL(try await client.connect()) }
                        catch { self.error = error.localizedDescription }
                    }
                }
                if connected {
                    Button("Disconnect Drive", role: .destructive) { disconnecting = true }
                }
            }
            if busy { ProgressView() }
        }
        .disabled(busy)
        .navigationTitle("Google Drive")
        .task(id: phase) { if phase == .active { await refresh() } }
        .refreshable { await refresh() }
        .confirmationDialog("Disconnect Drive?", isPresented: $disconnecting, titleVisibility: .visible) {
            Button("Disconnect", role: .destructive) {
                Task {
                    busy = true
                    let client = CompanionClient(connection: connection); defer { client.close() }
                    do { _ = try await client.request("drive/disconnect", method: "POST") }
                    catch { self.error = error.localizedDescription }
                    busy = false
                    await refresh()
                }
            }
        }
        .alert("Google Drive", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
    }

    private func refresh() async {
        guard !busy else { return }
        busy = true; defer { busy = false }
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            let capabilities = try await client.capabilities()
            connected = capabilities.driveConnected
            writable = capabilities.driveWriteAccess == true
            loaded = true
        } catch { self.error = error.localizedDescription }
    }
}
