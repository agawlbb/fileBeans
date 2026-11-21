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
    case image(URL, UIImage)
    case movie(Movie)

    static func preview(_ url: URL) -> Self {
        if UTType(filenameExtension: url.pathExtension)?.conforms(to: .image) == true,
           let image = UIImage(contentsOfFile: url.path) {
            return .image(url, image)
        }
        return .document(url)
    }
}

struct FolderViewerSession: Identifiable {
    let id = UUID()
    let files: [FolderViewerFile]
    let selected: String

    static func canView(name: String, media: Bool) -> Bool {
        // Folder entries are remote: Quick Look cannot inspect them before download.
        if media { return true }
        guard let type = UTType(filenameExtension: (name as NSString).pathExtension) else { return false }
        return [UTType.image, .pdf, .text, .rtf, .spreadsheet, .presentation,
                .compositeContent, .threeDContent].contains { type.conforms(to: $0) }
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

struct FolderViewerPage: View {
    let file: FolderViewerFile
    let active: Bool
    @State private var content: FolderViewerContent?
    @State private var error: String?

    var body: some View {
        // Keep lifecycle modifiers on a stable container, not the changing content.
        ZStack {
            if active, let content {
                switch content {
                case .document(let url): FolderDocumentPreview(url: url)
                case .image(_, let image): FolderImagePreview(image: image, name: file.name)
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
        case .document(let url), .image(let url, _):
            try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
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

/// A fitted photo leaves horizontal drags to the folder pager. Only zoomed photos pan.
struct FolderImagePreview: UIViewRepresentable {
    let image: UIImage
    let name: String

    func makeUIView(context: Context) -> FolderImageScrollView {
        let view = FolderImageScrollView()
        view.imageView.image = image
        view.imageView.isAccessibilityElement = true
        view.imageView.accessibilityLabel = name
        view.imageView.accessibilityTraits = .image
        return view
    }
    func updateUIView(_ view: FolderImageScrollView, context: Context) {}
}

final class FolderImageScrollView: UIScrollView, UIScrollViewDelegate {
    let imageView = UIImageView()
    private var layoutSize = CGSize.zero

    init() {
        super.init(frame: .zero)
        delegate = self
        minimumZoomScale = 1
        maximumZoomScale = 5
        showsHorizontalScrollIndicator = false
        showsVerticalScrollIndicator = false
        imageView.contentMode = .scaleAspectFit
        addSubview(imageView)
        let tap = UITapGestureRecognizer(target: self, action: #selector(toggleZoom(_:)))
        tap.numberOfTapsRequired = 2
        addGestureRecognizer(tap)
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard layoutSize != bounds.size else { return }
        layoutSize = bounds.size
        setZoomScale(minimumZoomScale, animated: false)
        imageView.frame = CGRect(origin: .zero, size: bounds.size)
        contentSize = bounds.size
    }
    override func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
        if gestureRecognizer === panGestureRecognizer, zoomScale <= minimumZoomScale + 0.01 { return false }
        return super.gestureRecognizerShouldBegin(gestureRecognizer)
    }
    func viewForZooming(in scrollView: UIScrollView) -> UIView? { imageView }
    @objc private func toggleZoom(_ gesture: UITapGestureRecognizer) {
        if zoomScale > minimumZoomScale + 0.01 {
            setZoomScale(minimumZoomScale, animated: true)
        } else {
            let point = gesture.location(in: imageView)
            let size = CGSize(width: bounds.width / 2, height: bounds.height / 2)
            zoom(to: CGRect(x: point.x - size.width / 2, y: point.y - size.height / 2,
                            width: size.width, height: size.height), animated: true)
        }
    }
}
