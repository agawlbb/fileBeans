import SwiftUI
import QuickLook
import UniformTypeIdentifiers

struct FolderViewerFile: Identifiable {
    let id: String
    let name: String
    let canPrefetch: Bool
    let load: () async throws -> FolderViewerContent

    init(id: String, name: String, media: Bool = false, load: @escaping () async throws -> FolderViewerContent) {
        self.id = id; self.name = name; self.load = load
        let type = UTType(filenameExtension: (name as NSString).pathExtension)
        canPrefetch = media || type?.conforms(to: .image) == true || type?.conforms(to: .audiovisualContent) == true
    }
}

enum FolderViewerContent {
    case document(URL)
    case image(URL, UIImage)
    case movie(Movie)

    @MainActor func release() {
        switch self {
        case .document(let url), .image(let url, _):
            try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
        case .movie(let movie): movie.stop(); movie.stream.clearCache()
        }
    }

    static func preview(_ url: URL) -> Self {
        if UTType(filenameExtension: url.pathExtension)?.conforms(to: .image) == true,
           let image = UIImage(contentsOfFile: url.path) {
            return .image(url, image)
        }
        return .document(url)
    }
}

/// Own downloads independently of SwiftUI's offscreen page lifetime.
@MainActor
final class FolderViewerCache: ObservableObject {
    @Published private(set) var contents: [String: FolderViewerContent] = [:]
    @Published private(set) var errors: [String: String] = [:]
    private let files: [FolderViewerFile]
    private var tasks: [String: Task<Void, Never>] = [:]
    private var wanted = Set<String>()
    private var selected: String?

    init(files: [FolderViewerFile]) { self.files = files }

    func select(_ id: String) {
        guard let index = files.firstIndex(where: { $0.id == id }) else { clear(); return }
        if selected != id, let selected, let content = contents[selected], case .movie(let movie) = content {
            movie.stop()
        }
        selected = id
        wanted = [id]
        for neighbor in [index - 1, index + 1] where files.indices.contains(neighbor) && files[neighbor].canPrefetch {
            wanted.insert(files[neighbor].id)
        }
        evict()
        // Speculative failures do not prevent a fresh foreground attempt.
        errors.removeValue(forKey: id)
        start(files[index])
        if contents[id] != nil { warmNeighbors() }
    }

    func retry(_ file: FolderViewerFile) { errors.removeValue(forKey: file.id); start(file) }

    func trimToSelected() {
        wanted = Set(selected.map { [$0] } ?? [])
        evict()
    }

    func clear() { selected = nil; wanted = []; evict() }

    private func evict() {
        for id in Array(tasks.keys) where !wanted.contains(id) { tasks.removeValue(forKey: id)?.cancel() }
        for id in Array(contents.keys) where !wanted.contains(id) { contents.removeValue(forKey: id)?.release() }
        errors = errors.filter { wanted.contains($0.key) }
    }

    private func warmNeighbors() {
        for file in files where wanted.contains(file.id) && file.id != selected { start(file) }
    }

    private func start(_ file: FolderViewerFile) {
        guard wanted.contains(file.id), tasks[file.id] == nil else { return }
        if let content = contents[file.id] {
            // A movie opened first has no speculative head yet. Warm it when
            // it becomes a neighbor too, so swiping backward benefits equally.
            if file.id != selected, case .movie(let movie) = content {
                tasks[file.id] = Task(priority: .utility) { @MainActor [weak self] in
                    try? await movie.stream.prefetch()
                    guard !Task.isCancelled else { return }
                    self?.tasks.removeValue(forKey: file.id)
                }
            }
            return
        }
        let speculative = file.id != selected
        tasks[file.id] = Task(priority: speculative ? .utility : .userInitiated) { @MainActor [weak self] in
            do {
                let loaded = try await file.load()
                if speculative, case .movie(let movie) = loaded { try? await movie.stream.prefetch() }
                guard let self, !Task.isCancelled, self.wanted.contains(file.id) else { loaded.release(); return }
                self.tasks.removeValue(forKey: file.id)
                self.contents[file.id] = loaded
                if self.selected == file.id { self.warmNeighbors() }
            } catch {
                guard let self, !Task.isCancelled else { return }
                self.tasks.removeValue(forKey: file.id)
                // A failed neighbor is retried when selected, without an offscreen alert.
                if self.selected == file.id { self.errors[file.id] = error.localizedDescription }
            }
        }
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

/// Multiple windows may independently keep media visible.
@MainActor
private enum MediaScreenAwake {
    static var viewers = Set<UUID>()

    static func update(_ viewer: UUID, enabled: Bool) {
        if enabled { viewers.insert(viewer) } else { viewers.remove(viewer) }
        UIApplication.shared.isIdleTimerDisabled = !viewers.isEmpty
    }
}

/// One presentation and player survive full-screen changes and folder paging.
struct FolderViewer: View {
    let session: FolderViewerSession
    @State private var selected: String
    @StateObject private var cache: FolderViewerCache
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase
    @State private var visible = false
    @State private var wakeID = UUID()
    @State private var fullScreen = false

    private func updateScreenAwake() {
        let media = session.files.first(where: { $0.id == selected })?.canPrefetch == true
        MediaScreenAwake.update(wakeID, enabled: visible && scenePhase == .active && media)
    }

    init(session: FolderViewerSession) {
        self.session = session
        _selected = State(initialValue: session.selected)
        _cache = StateObject(wrappedValue: FolderViewerCache(files: session.files))
    }

    var body: some View {
        ZStack {
            TabView(selection: $selected) {
                ForEach(session.files) { file in
                    FolderViewerPage(file: file, active: selected == file.id, cache: cache, fullScreen: fullScreen)
                        .ignoresSafeArea(.container, edges: fullScreen ? .all : [])
                        .tag(file.id)
                }
            }
            .tabViewStyle(.page(indexDisplayMode: .never))
        }
        .ignoresSafeArea(.container, edges: fullScreen ? .all : [])
        .background {
            if fullScreen { Color.black.ignoresSafeArea() }
            else { Color(uiColor: .systemBackground) }
        }
        .statusBarHidden(fullScreen)
        .persistentSystemOverlays(fullScreen ? .hidden : .automatic)
        .contentShape(Rectangle())
        .simultaneousGesture(TapGesture().onEnded {
            fullScreen = false
        }, including: fullScreen ? .all : .subviews)
        .onAppear { visible = true; cache.select(selected); updateScreenAwake() }
        .onChange(of: selected) { _, id in cache.select(id); updateScreenAwake() }
        .onChange(of: scenePhase) { _, _ in updateScreenAwake() }
        .onDisappear { visible = false; updateScreenAwake(); cache.clear() }
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didReceiveMemoryWarningNotification)) { _ in cache.trimToSelected() }
        .onReceive(NotificationCenter.default.publisher(for: .fileBeansProtectionChanged)) { _ in cache.clear(); dismiss() }
        .overlay(alignment: .topLeading) {
            if !fullScreen, session.files.first(where: { $0.id == selected })?.canPrefetch == true {
                Button {
                    fullScreen = true
                } label: {
                    Image(systemName: "arrow.up.left.and.arrow.down.right")
                        .frame(width: 44, height: 44)
                }
                .accessibilityLabel("Full Screen")
                .background(.regularMaterial, in: Capsule()).padding()
            }
        }
        .overlay(alignment: .topTrailing) {
            if !fullScreen {
                Button("Done") { dismiss() }
                    .padding().background(.regularMaterial, in: Capsule()).padding()
            }
        }
    }
}

struct FolderViewerPage: View {
    let file: FolderViewerFile
    let active: Bool
    @ObservedObject var cache: FolderViewerCache
    var fullScreen = false

    var body: some View {
        ZStack {
            if let content = cache.contents[file.id] {
                switch content {
                case .document(let url):
                    if active { FolderDocumentPreview(url: url) } else { ProgressView() }
                case .image(_, let image): FolderImagePreview(image: image, name: file.name)
                case .movie(let movie):
                    if active { MovieView(movie: movie, fullScreen: fullScreen).id(movie.id) } else { ProgressView() }
                }
            } else if let error = cache.errors[file.id] {
                ContentUnavailableView {
                    Label(file.name, systemImage: "doc")
                } description: { Text(error) } actions: {
                    Button("Retry") { cache.retry(file) }
                }
            } else { ProgressView() }
        }
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
        // SwiftUI owns the pager safe area; do not inset its image a second time.
        contentInsetAdjustmentBehavior = .never
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
