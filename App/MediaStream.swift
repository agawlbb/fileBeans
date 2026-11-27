import AVFoundation
import UniformTypeIdentifiers

/// Translate AVPlayer's byte requests into bounded filesystem stream requests.
/// The delegate and loading-request state are confined to the main queue.
@MainActor
final class MediaStream: NSObject, @preconcurrency AVAssetResourceLoaderDelegate {
    private let fetch: (URLRequest, Int64, Int) -> AsyncThrowingStream<MediaChunk, Error>
    private let request: URLRequest
    private let name: String
    private var tasks: [ObjectIdentifier: Task<Void, Never>] = [:]
    private(set) var loadingRequestCount = 0
    private(set) var completedRequestCount = 0
    private(set) var suppliedByteCount: Int64 = 0
    private let ranges = MediaRangeCache()

    // Warm only the opening bytes of an adjacent video; playback fills the range cache.
    func prefetch() async throws {
        _ = try await readChunk(offset: 0, length: 256 * 1024)
    }

    func clearCache() { ranges.clear() }

    func readChunk(offset: Int64, length: Int) async throws -> MediaChunk {
        try Task.checkCancellation()
        var data = Data()
        var metadata: MediaChunk?
        try await read(offset: offset, length: Int64(length)) { chunk in
            data.append(chunk.data); metadata = chunk
        }
        guard let metadata else { throw URLError(.badServerResponse) }
        return MediaChunk(data: data, totalLength: metadata.totalLength, mimeType: metadata.mimeType)
    }

    func read(offset: Int64, length: Int64?, receive: (MediaChunk) throws -> Void) async throws {
        guard offset >= 0, length.map({ $0 > 0 && $0 <= Int64.max - offset }) ?? true else { throw URLError(.badURL) }
        var cursor = offset
        var end = length.map { offset + $0 } ?? Int64.max
        while cursor < end {
            try Task.checkCancellation()
            let before = cursor
            let count = Int(min(Int64(MediaRange.chunkSize), end - cursor))
            try await ranges.read(offset: cursor, length: count, fetch: { offset, length in
                self.fetch(self.request, offset, length)
            }) { chunk in
                try receive(chunk)
                cursor += Int64(chunk.data.count); end = min(end, chunk.totalLength)
            }
            guard cursor > before else { throw URLError(.badServerResponse) }
        }
    }

    init(client: FilesystemClient, request: URLRequest, name: String) {
        self.fetch = { client.streamChunks($0, offset: $1, length: $2) }; self.request = request; self.name = name
    }

    init(client: CompanionClient, request: URLRequest, name: String) {
        self.fetch = { client.streamChunks($0, offset: $1, length: $2) }; self.request = request; self.name = name
    }

    func asset() -> AVURLAsset {
        // Keep authenticated server URLs out of the player and its error logs.
        let url = URL(string: "filebeans-media://playback/" + UUID().uuidString)!
            .appendingPathExtension((name as NSString).pathExtension)
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(self, queue: .main)
        return asset
    }

    func play(on player: AVPlayer) async throws {
        let asset = asset()
        let playable = try await withTaskCancellationHandler {
            try await asset.load(.isPlayable)
        } onCancel: {
            asset.cancelLoading()
        }
        try Task.checkCancellation()
        guard playable else { throw AVError(.fileFormatNotRecognized) }
        player.automaticallyWaitsToMinimizeStalling = false
        player.replaceCurrentItem(with: AVPlayerItem(asset: asset))
        player.play()
    }

    func cancel() {
        for task in tasks.values { task.cancel() }
        tasks.removeAll()
        ranges.cancel()
    }

    func resourceLoader(_ resourceLoader: AVAssetResourceLoader,
                        shouldWaitForLoadingOfRequestedResource loading: AVAssetResourceLoadingRequest) -> Bool {
        let id = ObjectIdentifier(loading)
        loadingRequestCount += 1
        tasks[id] = Task { @MainActor in
            defer { tasks.removeValue(forKey: id) }
            do {
                let dataRequest = loading.dataRequest
                let offset = dataRequest.map { max($0.requestedOffset, $0.currentOffset) } ?? 0
                let length: Int64?
                if let dataRequest {
                    length = dataRequest.requestsAllDataToEndOfResource ? nil
                        : Int64(dataRequest.requestedLength) - (offset - dataRequest.requestedOffset)
                } else {
                    length = 2 // Content information probe; never a full-file GET.
                }
                if let length, length <= 0 {
                    if !loading.isCancelled { loading.finishLoading() }
                    return
                }
                try await self.read(offset: offset, length: length, receive: { chunk in
                    try Task.checkCancellation()
                    guard !loading.isCancelled else { throw CancellationError() }
                    if let info = loading.contentInformationRequest {
                        info.contentLength = chunk.totalLength
                        info.contentType = chunk.mimeType.flatMap { UTType(mimeType: $0)?.identifier }
                            ?? UTType(filenameExtension: (self.name as NSString).pathExtension)?.identifier
                            ?? UTType.data.identifier
                        info.isByteRangeAccessSupported = true
                        // Only recently fetched ranges are cached, not the complete movie.
                        info.isEntireLengthAvailableOnDemand = false
                    }
                    self.suppliedByteCount += Int64(chunk.data.count)
                    dataRequest?.respond(with: chunk.data)
                })
                try Task.checkCancellation()
                if !loading.isCancelled { completedRequestCount += 1; loading.finishLoading() }
            } catch {
                if !Task.isCancelled, !loading.isCancelled { loading.finishLoading(with: error) }
            }
        }
        return true
    }

    func resourceLoader(_ resourceLoader: AVAssetResourceLoader, didCancel loading: AVAssetResourceLoadingRequest) {
        tasks.removeValue(forKey: ObjectIdentifier(loading))?.cancel()
    }
}
