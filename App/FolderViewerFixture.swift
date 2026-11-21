#if DEBUG
import SwiftUI

/// Isolated synthetic gallery for gesture tests; absent from Release builds.
struct FolderViewerFixture: View {
    @State private var session: FolderViewerSession?
    var body: some View {
        Button("Open Viewer") {
            let files = [("first.png", UIColor.blue), ("second.png", UIColor.red), ("third.png", UIColor.green)].map { name, color in
                FolderViewerFile(id: name, name: name) {
                    let data = UIGraphicsImageRenderer(size: CGSize(width: 480, height: 640)).pngData { context in
                        color.setFill()
                        context.fill(CGRect(x: 0, y: 0, width: 480, height: 640))
                    }
                    let url = try await FolderViewerSession.prepare(name: name) {
                        let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
                        try data.write(to: source)
                        return source
                    }
                    return .preview(url)
                }
            }
            session = FolderViewerSession(files: files, selected: "first.png")
        }
        .fullScreenCover(item: $session) { FolderViewer(session: $0) }
    }
}
#endif
