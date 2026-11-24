import SwiftUI
import UIKit

struct HostedFile: Decodable, Identifiable {
    let id: String
    let name: String
    let created: Int64
    let expires: Int64
    let state: String
    let urlPath: String

    func url(connection: Connection) -> URL? {
        guard !urlPath.isEmpty, let base = connection.companionURL,
              var parts = URLComponents(url: base, resolvingAgainstBaseURL: false) else { return nil }
        let prefix = parts.percentEncodedPath.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        parts.percentEncodedPath = (prefix.isEmpty ? "" : "/" + prefix) + urlPath
        parts.query = nil; parts.fragment = nil
        return parts.url
    }
}

extension CompanionClient {
    func hostedFiles() async throws -> [HostedFile] {
        try JSONDecoder().decode([HostedFile].self, from: await request("hosting"))
    }
    func hostFile(_ item: CopyLocation, expires: Int64) async throws -> HostedFile {
        struct Request: Encodable { let item: CopyLocation; let expires: Int64 }
        let canonical = try sourceLocation(item)
        return try JSONDecoder().decode(HostedFile.self, from: await request("hosting", method: "POST",
            body: JSONEncoder().encode(Request(item: canonical, expires: expires))))
    }
    func updateHosting(_ id: String, expires: Int64) async throws -> HostedFile {
        try JSONDecoder().decode(HostedFile.self, from: await request("hosting/" + id, method: "PATCH",
            body: JSONEncoder().encode(["expires": expires])))
    }
    func stopHosting(_ id: String) async throws {
        _ = try await request("hosting/" + id, method: "DELETE")
    }
}

struct HostingEditor: View {
    let connection: Connection
    let item: CopyLocation?
    let existing: HostedFile?
    var completed: () -> Void = {}
    @Environment(\.dismiss) private var dismiss
    @State private var limited = true
    @State private var expiry = Date().addingTimeInterval(7 * 24 * 60 * 60)
    @State private var hosted: HostedFile?
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Text(existing?.name ?? item?.name ?? "File")
                if let hosted, let url = hosted.url(connection: connection) {
                    HostingLink(url: url)
                } else {
                    Picker("Duration", selection: $limited) {
                        Text("Time Limited").tag(true)
                        Text("Indefinite").tag(false)
                    }
                    if limited {
                        DatePicker("Expires", selection: $expiry, in: Date()..., displayedComponents: [.date, .hourAndMinute])
                    }
                    Button(existing == nil ? "Host File" : "Save") { Task { await save() } }
                        .disabled(busy || limited && expiry <= Date())
                }
                if let error { Text(error).foregroundStyle(.red) }
            }
            .disabled(busy)
            .overlay { if busy { ProgressView() } }
            .navigationTitle("Public Hosting")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Done") { dismiss() }.disabled(busy) } }
        }
        .interactiveDismissDisabled(busy)
        .onAppear {
            if let existing {
                limited = existing.expires != 0
                if existing.expires > Int64(Date().timeIntervalSince1970) { expiry = Date(timeIntervalSince1970: Double(existing.expires)) }
            }
        }
    }
    private func save() async {
        guard !busy else { return }
        busy = true; defer { busy = false }
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            let expires = limited ? Int64(expiry.timeIntervalSince1970) : 0
            if let existing { hosted = try await client.updateHosting(existing.id, expires: expires) }
            else if let item { hosted = try await client.hostFile(item, expires: expires) }
            completed()
        } catch { self.error = error.localizedDescription }
    }
}

private struct HostingLink: View {
    let url: URL
    var body: some View {
        Text(url.absoluteString).font(.footnote).textSelection(.enabled)
        Button("Copy Link", systemImage: "link") { UIPasteboard.general.url = url }
        ShareLink("Share Link", item: url)
    }
}

struct HostingSettingsView: View {
    let connection: Connection
    @State private var files: [HostedFile] = []
    @State private var editing: HostedFile?
    @State private var stopping: HostedFile?
    @State private var busy = false
    @State private var loaded = false
    @State private var error: String?
    var body: some View {
        List {
            ForEach(files) { file in
                Section {
                    Text(file.name)
                    if file.state == "expired" { Text("Expired").foregroundStyle(.secondary) }
                    else if file.state == "unavailable" { Text("Unavailable").foregroundStyle(.secondary) }
                    else if file.expires == 0 { Text("Indefinite").foregroundStyle(.secondary) }
                    else { LabeledContent("Expires") { Text(Date(timeIntervalSince1970: Double(file.expires)), format: .dateTime) } }
                    if let url = file.url(connection: connection) { HostingLink(url: url) }
                    if file.state != "unavailable" { Button("Change Duration") { editing = file } }
                    Button("Stop Hosting", role: .destructive) { stopping = file }
                }
            }
        }
        .navigationTitle("Public Hosting")
        .disabled(busy)
        .overlay {
            if busy { ProgressView() }
            else if loaded && files.isEmpty && error == nil { ContentUnavailableView("No Hosted Files", systemImage: "link") }
        }
        .task { await reload() }
        .refreshable { await reload() }
        .sheet(item: $editing) { file in
            HostingEditor(connection: connection, item: nil, existing: file) { Task { await reload() } }
        }
        .confirmationDialog("Stop Hosting?", isPresented: Binding(get: { stopping != nil }, set: { if !$0 { stopping = nil } })) {
            Button("Stop Hosting", role: .destructive) { if let file = stopping { stopping = nil; Task { await stop(file) } } }
        }
        .alert("Public Hosting", isPresented: Binding(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: { Text(error ?? "") }
    }
    private func reload() async {
        guard !busy else { return }
        busy = true; defer { busy = false }
        let client = CompanionClient(connection: connection); defer { client.close() }
        do {
            guard try await client.capabilities().features?.contains("publicHostingV1") == true else { throw APIError.incompatible }
            files = try await client.hostedFiles(); loaded = true; error = nil
        } catch { self.error = error.localizedDescription }
    }
    private func stop(_ file: HostedFile) async {
        busy = true
        let client = CompanionClient(connection: connection); defer { client.close() }
        do { try await client.stopHosting(file.id); files.removeAll { $0.id == file.id } }
        catch { self.error = error.localizedDescription }
        busy = false
    }
}
