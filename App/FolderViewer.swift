import SwiftUI
import QuickLook
import UniformTypeIdentifiers

struct FolderViewerFile: Identifiable {
    let id: String
    let name: String
    let load: () async throws -> FolderViewerContent
}

enum FolderViewerContent {
    case document(URL)
    case movie(Movie)
}

struct FolderViewerSession: Identifiable {
    let id = UUID()
    let files: [FolderViewerFile]
    let selected: String

    static func canView(name: String, media: Bool) -> Bool {
        media || QLPreviewController.canPreview(URL(fileURLWithPath: name) as NSURL)
    }

    static func prepare(name: String, download: () async throws -> URL) async throws -> URL {
        _ = try FilePath.child(name, in: "/")
        let file = try await download()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        do {
            try Task.checkCancellation()
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            let target = directory.appendingPathComponent(name)
            try FileManager.default.moveItem(at: file, to: target)
            return target
        } catch {
            try? FileManager.default.removeItem(at: file)
            try? FileManager.default.removeItem(at: directory)
            throw error
        }
    }
}

/// One presentation contains every file, so paging never exits full screen.
struct FolderViewer: View {
    let session: FolderViewerSession
    @State private var selected: String
    @Environment(\.dismiss) private var dismiss

    init(session: FolderViewerSession) {
        self.session = session
        _selected = State(initialValue: session.selected)
    }

    var body: some View {
        TabView(selection: $selected) {
            ForEach(session.files) { file in
                FolderViewerPage(file: file, active: selected == file.id)
                    .tag(file.id)
            }
        }
        .tabViewStyle(.page(indexDisplayMode: .never))
        .overlay(alignment: .topTrailing) {
            Button("Done") { dismiss() }
                .padding().background(.regularMaterial, in: Capsule()).padding()
        }
    }
}

private struct FolderViewerPage: View {
    let file: FolderViewerFile
    let active: Bool
    @State private var content: FolderViewerContent?
    @State private var error: String?

    var body: some View {
        Group {
            if active, let content {
                switch content {
                case .document(let url): FolderDocumentPreview(url: url)
                case .movie(let movie): MovieView(movie: movie).id(movie.id)
                }
            } else if let error {
                ContentUnavailableView {
                    Label(file.name, systemImage: "doc")
                } description: { Text(error) } actions: {
                    Button("Retry") { self.error = nil }
                }
            } else { ProgressView() }
        }
        .task(id: "\(active)-\(error == nil)") {
            guard active, content == nil, error == nil else { return }
            do {
                let loaded = try await file.load()
                if Task.isCancelled { release(loaded); return }
                content = loaded
            } catch {
                if !Task.isCancelled { self.error = error.localizedDescription }
            }
        }
        .onChange(of: active) { _, active in
            if !active { clear() }
        }
        .onDisappear { clear() }
    }

    private func release(_ content: FolderViewerContent) {
        switch content {
        case .document(let url): try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
        case .movie(let movie): movie.stream.cancel()
        }
    }
    private func clear() {
        if let content { release(content) }
        content = nil
        error = nil
    }
}

private struct FolderDocumentPreview: UIViewControllerRepresentable {
    let url: URL
    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        let url: URL
        init(url: URL) { self.url = url }
        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem { url as NSURL }
    }
    func makeCoordinator() -> Coordinator { Coordinator(url: url) }
    func makeUIViewController(context: Context) -> QLPreviewController {
        let controller = QLPreviewController()
        controller.dataSource = context.coordinator
        return controller
    }
    func updateUIViewController(_ controller: QLPreviewController, context: Context) {}
}
