import XCTest
import SwiftUI
import QuickLook
import UIKit
@testable import myFiles

@MainActor
final class FolderViewerTests: XCTestCase {
    func testAdjacentImagesAreReusedAndEvictedOutsideWindow() async throws {
        var loads: [String] = []
        var urls: [String: URL] = [:]
        let files = (0..<5).map { index in
            let id = String(index)
            return FolderViewerFile(id: id, name: id + ".png") {
                loads.append(id)
                let url = try await self.photoURL(name: id + ".png")
                urls[id] = url
                return .preview(url)
            }
        }
        let cache = FolderViewerCache(files: files)
        defer { cache.clear() }
        cache.select("2")
        try await waitUntil { cache.contents.count == 3 }
        XCTAssertEqual(loads.first, "2", "Visible content must finish before speculative downloads start")
        XCTAssertEqual(Set(loads), Set(["1", "2", "3"]))
        cache.select("3")
        try await waitUntil { cache.contents["4"] != nil }
        XCTAssertEqual(Set(cache.contents.keys), Set(["2", "3", "4"]))
        XCTAssertEqual(loads.filter { $0 == "3" }.count, 1, "Swiping must reuse the neighbor download")
        XCTAssertFalse(FileManager.default.fileExists(atPath: try XCTUnwrap(urls["1"]).path))
        cache.clear()
        XCTAssertTrue(urls.values.allSatisfy { !FileManager.default.fileExists(atPath: $0.path) })
    }

    func testDocumentsAreNotPrefetchedAndWindowStopsAtFolderEdges() async throws {
        var loads: [String] = []
        let files = ["photo.png", "notes.txt", "last.png"].map { name in
            FolderViewerFile(id: name, name: name) {
                loads.append(name)
                return .preview(try await self.photoURL(name: name))
            }
        }
        let cache = FolderViewerCache(files: files)
        defer { cache.clear() }
        cache.select("photo.png")
        try await waitUntil { cache.contents["photo.png"] != nil }
        XCTAssertEqual(loads, ["photo.png"])
        cache.select("notes.txt")
        try await waitUntil { cache.contents["last.png"] != nil }
        XCTAssertEqual(Set(loads), Set(files.map(\.id)))
        cache.trimToSelected()
        XCTAssertEqual(Set(cache.contents.keys), Set(["notes.txt"]))
    }

    func testCancelledPrefetchCleansUpLateDownload() async throws {
        var pending: CheckedContinuation<FolderViewerContent, Never>?
        let first = FolderViewerFile(id: "first", name: "first.png") {
            .preview(try await self.photoURL(name: "first.png"))
        }
        let neighbor = FolderViewerFile(id: "neighbor", name: "neighbor.png") {
            await withCheckedContinuation { pending = $0 }
        }
        let cache = FolderViewerCache(files: [first, neighbor])
        defer { cache.clear() }
        cache.select("first")
        try await waitUntil { pending != nil }
        cache.clear()
        let url = try await photoURL(name: "neighbor.png")
        try XCTUnwrap(pending).resume(returning: .preview(url))
        try await waitUntil { !FileManager.default.fileExists(atPath: url.path) }
        XCTAssertTrue(cache.contents.isEmpty)
        XCTAssertTrue(cache.errors.isEmpty)
    }

    private func waitUntil(_ condition: () -> Bool) async throws {
        for _ in 0..<200 {
            if condition() { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTFail("Viewer loading did not settle")
    }

    private func photoURL(name: String) async throws -> URL {
        let png = UIGraphicsImageRenderer(size: CGSize(width: 4, height: 4)).pngData { context in
            UIColor.blue.setFill(); context.fill(CGRect(x: 0, y: 0, width: 4, height: 4))
        }
        return try await FolderViewerSession.prepare(name: name) {
            let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
            try png.write(to: url)
            return url
        }
    }

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
        let cache = FolderViewerCache(files: [file])
        cache.select(file.id)
        defer { cache.clear() }
        let host = UIHostingController(rootView: FolderViewerPage(file: file, active: true, cache: cache))
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
