import XCTest
import SwiftUI
import QuickLook
@testable import myFiles

@MainActor
final class FolderViewerTests: XCTestCase {
    func testRemotePhotosAndDocumentsDoNotRequireLocalFiles() {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        XCTAssertFalse(FileManager.default.fileExists(atPath: directory.path))
        for name in ["photo.jpg", "photo.jpeg", "photo.PNG", "photo.heic", "photo.gif", "scan.pdf", "notes.txt", "report.docx", "sheet.xlsx", "slides.pptx"] {
            XCTAssertTrue(FolderViewerSession.canView(name: directory.appendingPathComponent(name).path, media: false), name)
        }
        XCTAssertFalse(FolderViewerSession.canView(name: "archive.zip", media: false))
        XCTAssertFalse(FolderViewerSession.canView(name: "program.exe", media: false))
        XCTAssertTrue(FolderViewerSession.canView(name: "stream", media: true))
    }

    func testDownloadedPhotoSurvivesTransitionFromLoadingToPreview() async throws {
        let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let png = UIGraphicsImageRenderer(size: CGSize(width: 40, height: 30)).pngData { context in
            UIColor.blue.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 40, height: 30))
        }
        try png.write(to: source)
        var prepared: URL?
        var loads = 0
        let file = FolderViewerFile(id: "photo", name: "photo.png") {
            loads += 1
            let url = try await FolderViewerSession.prepare(name: "photo.png") { source }
            prepared = url
            return .document(url)
        }
        let host = UIHostingController(rootView: FolderViewerPage(file: file, active: true))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            window.rootViewController = nil
            try? FileManager.default.removeItem(at: source)
            if let prepared { try? FileManager.default.removeItem(at: prepared.deletingLastPathComponent()) }
        }
        var preview: QLPreviewController?
        for _ in 0..<100 {
            preview = findPreview(in: host)
            if preview != nil { break }
            try await Task.sleep(for: .milliseconds(50))
        }
        let controller = try XCTUnwrap(preview, "Loading must transition to the photo preview")
        // Allow appearance callbacks after the loading view has been replaced.
        try await Task.sleep(for: .milliseconds(500))
        let url = try XCTUnwrap(prepared)
        XCTAssertEqual(loads, 1)
        XCTAssertTrue(FileManager.default.fileExists(atPath: url.path), "Preview must retain its downloaded photo")
        XCTAssertEqual(try Data(contentsOf: url), png)
        XCTAssertNotNil(UIImage(contentsOfFile: url.path))
        XCTAssertEqual(controller.dataSource?.numberOfPreviewItems(in: controller), 1)
        XCTAssertEqual(controller.dataSource?.previewController(controller, previewItemAt: 0).previewItemURL, url)
    }

    private func findPreview(in controller: UIViewController) -> QLPreviewController? {
        if let preview = controller as? QLPreviewController { return preview }
        for child in controller.children {
            if let preview = findPreview(in: child) { return preview }
        }
        return nil
    }
}
