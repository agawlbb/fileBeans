import XCTest
import SwiftUI
import QuickLook
import UIKit
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
            return .preview(url)
        }
        let host = UIHostingController(rootView: FolderViewerPage(file: file, active: true))
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let previousKeyWindow = scene.windows.first { $0.isKeyWindow }
        let window = UIWindow(windowScene: scene)
        window.frame = scene.coordinateSpace.bounds
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.loadViewIfNeeded()
        host.view.layoutIfNeeded()
        defer {
            window.isHidden = true
            window.rootViewController = nil
            previousKeyWindow?.makeKey()
            try? FileManager.default.removeItem(at: source)
            if let prepared { try? FileManager.default.removeItem(at: prepared.deletingLastPathComponent()) }
        }
        var preview: UIImageView?
        for _ in 0..<100 {
            preview = findPhoto(in: host.view)
            if preview != nil { break }
            try await Task.sleep(for: .milliseconds(50))
        }
        XCTAssertEqual(loads, 1, "The hosted page must start loading")
        XCTAssertNotNil(prepared, "The download must finish")
        let controller = try XCTUnwrap(preview, "Loading must transition to the photo preview; children: \(host.children)")
        // Allow appearance callbacks after the loading view has been replaced.
        try await Task.sleep(for: .milliseconds(500))
        let url = try XCTUnwrap(prepared)
        XCTAssertEqual(loads, 1)
        XCTAssertTrue(FileManager.default.fileExists(atPath: url.path), "Preview must retain its downloaded photo")
        XCTAssertEqual(try Data(contentsOf: url), png)
        XCTAssertNotNil(UIImage(contentsOfFile: url.path))
        XCTAssertNotNil(controller.image)
        XCTAssertEqual(controller.accessibilityLabel, "photo.png")
    }

    private func findPhoto(in view: UIView) -> UIImageView? {
        if let image = view as? UIImageView, image.accessibilityLabel == "photo.png" { return image }
        for child in view.subviews {
            if let image = findPhoto(in: child) { return image }
        }
        return nil
    }
}
