import SwiftUI
import UniformTypeIdentifiers

final class ShareViewController: UIViewController {
    override func viewDidLoad() {
        super.viewDidLoad()
        let host = UIHostingController(rootView: ShareUploadView(context: extensionContext))
        addChild(host); view.addSubview(host.view)
        host.view.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            host.view.leadingAnchor.constraint(equalTo: view.leadingAnchor),
            host.view.trailingAnchor.constraint(equalTo: view.trailingAnchor),
            host.view.topAnchor.constraint(equalTo: view.topAnchor),
            host.view.bottomAnchor.constraint(equalTo: view.bottomAnchor)
        ])
        host.didMove(toParent: self)
    }
}

struct ShareUploadView: View {
    let context: NSExtensionContext?
    @State private var connections: [Connection] = []
    @State private var files: [URL] = []
    @State private var directory: URL?
    @State private var error: String?
    @State private var loading = true
    @State private var submitting = false
    var body: some View {
        NavigationStack {
            List {
                if loading || submitting { ProgressView() }
                if let error { Text(error).foregroundStyle(.red) }
                if !loading, !submitting {
                    if connections.isEmpty { Text("Add a connection in fileBeans") }
                    ForEach(connections) { connection in
                        NavigationLink(connection.name) {
                            SourceDestinationPicker(connection: connection, mode: .upload,
                                excludedLocal: [], excludedDrive: []) { destination in
                                upload(connection, destination: destination)
                            }
                        }.disabled(files.isEmpty)
                    }
                }
            }
            .navigationTitle("Upload To")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } } }
            .task { await load() }
        }
    }
    private func load() async {
        guard directory == nil else { return }
        do {
            connections = try SharedStore().connections().map(\.unified)
            let root = try AppConfiguration.directory.appendingPathComponent("share-imports/" + UUID().uuidString)
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            directory = root
            let providers = (context?.inputItems as? [NSExtensionItem] ?? []).flatMap { $0.attachments ?? [] }
            guard !providers.isEmpty else { throw CocoaError(.fileReadUnknown) }
            for (index, provider) in providers.enumerated() {
                let container = root.appendingPathComponent(String(index))
                try FileManager.default.createDirectory(at: container, withIntermediateDirectories: true)
                let copy: URL
                if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
                    copy = try await withCheckedThrowingContinuation { continuation in
                        provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { value, error in
                            do {
                                let source: URL?
                                if let url = value as? URL { source = url }
                                else if let data = value as? Data { source = URL(dataRepresentation: data, relativeTo: nil) }
                                else { source = nil }
                                guard let source, source.isFileURL else { throw error ?? CocoaError(.fileReadUnknown) }
                                let access = source.startAccessingSecurityScopedResource()
                                defer { if access { source.stopAccessingSecurityScopedResource() } }
                                _ = try FilePath.child(source.lastPathComponent, in: "/")
                                let target = container.appendingPathComponent(source.lastPathComponent)
                                try FileManager.default.copyItem(at: source, to: target)
                                continuation.resume(returning: target)
                            } catch { continuation.resume(throwing: error) }
                        }
                    }
                } else {
                    guard let type = provider.registeredTypeIdentifiers.first(where: {
                        guard let type = UTType($0) else { return false }
                        return type.conforms(to: .data) && !type.conforms(to: .url)
                    }) else { throw CocoaError(.fileReadUnsupportedScheme) }
                    copy = try await withCheckedThrowingContinuation { continuation in
                        provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                            do {
                                guard let url else { throw error ?? CocoaError(.fileReadUnknown) }
                                var name = provider.suggestedName ?? url.lastPathComponent
                                if (name as NSString).pathExtension.isEmpty, let ext = UTType(type)?.preferredFilenameExtension { name += "." + ext }
                                _ = try FilePath.child(name, in: "/")
                                let target = container.appendingPathComponent(name)
                                try FileManager.default.copyItem(at: url, to: target)
                                continuation.resume(returning: target)
                            } catch { continuation.resume(throwing: error) }
                        }
                    }
                }
                files.append(copy)
            }
        } catch { files = []; self.error = error.localizedDescription; cleanup() }
        loading = false
    }
    private func upload(_ connection: Connection, destination: BrowserDestination) {
        guard !submitting else { return }
        submitting = true
        Task {
            do {
                switch destination {
                case .local(let folder):
                    guard folder.permissions.create, folder.protection?.unlocked != false else { throw APIError.forbidden }
                    try await UploadQueue.shared.enqueue(files, connection: connection, source: folder.source, parent: folder.file.path, scoped: false)
                case .drive(let folder):
                    guard folder.capabilities?.canAddChildren == true else { throw APIError.forbidden }
                    try await UploadQueue.shared.enqueue(files, connection: connection, source: "drive", parent: folder.id, scoped: false)
                }
                await UploadQueue.shared.waitForScheduling()
                cleanup()
                context?.completeRequest(returningItems: nil)
            } catch { self.error = error.localizedDescription; submitting = false }
        }
    }
    private func cleanup() { if let directory { try? FileManager.default.removeItem(at: directory) } }
    private func cancel() {
        guard !submitting else { return }
        cleanup()
        context?.cancelRequest(withError: CocoaError(.userCancelled))
    }
}
