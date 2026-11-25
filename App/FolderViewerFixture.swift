#if DEBUG
import SwiftUI
import AVFoundation

/// Isolated synthetic gallery for gesture tests; absent from Release builds.
struct FolderViewerFixture: View {
    @State private var session: FolderViewerSession?
    var body: some View {
        Button("Open Viewer") {
            if ProcessInfo.processInfo.arguments.contains("--folder-viewer-video-test") {
                let configuration = URLSessionConfiguration.ephemeral
                configuration.protocolClasses = [FolderVideoProtocol.self]
                let client = FilesystemClient(connection: Connection(name: "Fixture", url: URL(string: "https://fixture.invalid")!),
                                              token: "synthetic-token", configuration: configuration)
                let file = FolderViewerFile(id: "video", name: "playback-probe.mp4", media: true) {
                    let request = try await client.streamRequest(source: "disk", path: "/playback-probe.mp4")
                    return .movie(Movie(name: "playback-probe.mp4",
                                        stream: MediaStream(client: client, request: request, name: "playback-probe.mp4")))
                }
                session = FolderViewerSession(files: [file], selected: file.id)
                return
            }
            let files = [("first.png", UIColor.blue), ("second.png", UIColor.red), ("third.png", UIColor.green)].map { name, color in
                FolderViewerFile(id: name, name: name) {
                    // The expansion regression needs a height-limited image;
                    // a width-limited photo can retain its visible size in both modes.
                    let size = ProcessInfo.processInfo.arguments.contains("--folder-viewer-tall-photo-test")
                        ? CGSize(width: 480, height: 1440) : CGSize(width: 480, height: 640)
                    let data = UIGraphicsImageRenderer(size: size).pngData { context in
                        color.setFill()
                        context.fill(CGRect(origin: .zero, size: size))
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

/// Synthetic authenticated range transport, isolated from production networking.
private final class FolderVideoProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "fixture.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            guard let url = Bundle.main.url(forResource: "playback-probe", withExtension: "mp4"),
                  let range = request.value(forHTTPHeaderField: "Range") else { throw URLError(.badURL) }
            let content = try Data(contentsOf: url)
            let bounds = range.dropFirst(6).split(separator: "-")
            guard bounds.count == 2, let start = Int(bounds[0]), let requestedEnd = Int(bounds[1]),
                  start >= 0, start < content.count else { throw URLError(.badServerResponse) }
            let end = min(requestedEnd, start + 32767, content.count - 1)
            let response = HTTPURLResponse(url: request.url!, statusCode: 206, httpVersion: "HTTP/1.1", headerFields: [
                "Content-Type": "video/mp4", "Content-Range": "bytes \(start)-\(end)/\(content.count)"
            ])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: content.subdata(in: start..<end + 1))
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

/// UI tests verify decoded motion and item identity through the real viewer.
struct FolderVideoPlaybackProbe: View {
    let movie: Movie
    @State private var status = "loading"
    var body: some View {
        Text(status).font(.caption2)
            .accessibilityIdentifier("Playback Probe")
            .allowsHitTesting(false)
            .onReceive(NotificationCenter.default.publisher(for: .AVPlayerItemDidPlayToEndTime)) { notification in
                guard let item = notification.object as? AVPlayerItem, item === movie.player.currentItem else { return }
                movie.player.seek(to: .zero) { finished in
                    if finished { movie.player.play() }
                }
            }
            .task {
                while movie.player.currentItem == nil {
                    do { try await Task.sleep(for: .milliseconds(100)) } catch { return }
                }
                guard let item = movie.player.currentItem else { return }
                let output = AVPlayerItemVideoOutput(pixelBufferAttributes: nil)
                item.add(output)
                defer { item.remove(output) }
                var frames = 0
                while !Task.isCancelled {
                    let time = movie.player.currentTime()
                    if output.hasNewPixelBuffer(forItemTime: time),
                       output.copyPixelBuffer(forItemTime: time, itemTimeForDisplay: nil) != nil { frames += 1 }
                    status = "time=\(time.seconds);frames=\(frames);item=\(ObjectIdentifier(item));attached=\(movie.player.currentItem === item)"
                    do { try await Task.sleep(for: .milliseconds(100)) } catch { return }
                }
            }
    }
}
#endif
