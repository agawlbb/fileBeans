import AVFoundation
import UniformTypeIdentifiers

/// Translate AVPlayer's byte requests into bounded filesystem stream requests.
/// The delegate and loading-request state are confined to the main queue.
@MainActor
final class MediaStream: NSObject, @preconcurrency AVAssetResourceLoaderDelegate {
    private let fetch: (URLRequest, Int64, Int) async throws -> MediaChunk
    private let request: URLRequest
    private let name: String
    private var tasks: [ObjectIdentifier: Task<Void, Never>] = [:]
    private(set) var loadingRequestCount = 0
    private(set) var completedRequestCount = 0
    private(set) var suppliedByteCount: Int64 = 0

    init(client: FilesystemClient, request: URLRequest, name: String) {
        self.fetch = { try await client.streamChunk($0, offset: $1, length: $2) }; self.request = request; self.name = name
    }

    init(client: CompanionClient, request: URLRequest, name: String) {
        self.fetch = { try await client.streamChunk($0, offset: $1, length: $2) }; self.request = request; self.name = name
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
        player.replaceCurrentItem(with: AVPlayerItem(asset: asset))
        player.play()
    }

    func cancel() {
        for task in tasks.values { task.cancel() }
        tasks.removeAll()
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
                try await MediaRange.read(offset: offset, length: length, firstChunkSize: 64 * 1024, fetch: { offset, count in
                    try await self.fetch(self.request, offset, count)
                }, receive: { chunk in
                    try Task.checkCancellation()
                    guard !loading.isCancelled else { throw CancellationError() }
                    if let info = loading.contentInformationRequest {
                        info.contentLength = chunk.totalLength
                        info.contentType = chunk.mimeType.flatMap { UTType(mimeType: $0)?.identifier }
                            ?? UTType(filenameExtension: (self.name as NSString).pathExtension)?.identifier
                            ?? UTType.data.identifier
                        info.isByteRangeAccessSupported = true
                        // These bytes arrive over HTTPS, not from a complete local cache.
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
