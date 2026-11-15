import AVFoundation
import UniformTypeIdentifiers

/// Translate AVPlayer's byte requests into bounded Quantum stream requests.
/// The delegate and loading-request state are confined to the main queue.
@MainActor
final class MediaStream: NSObject, AVAssetResourceLoaderDelegate {
    private let client: QuantumClient
    private let request: URLRequest
    private let name: String
    private var tasks: [ObjectIdentifier: Task<Void, Never>] = [:]

    init(client: QuantumClient, request: URLRequest, name: String) {
        self.client = client; self.request = request; self.name = name
    }

    func asset() -> AVURLAsset {
        // Keep authenticated server URLs out of the player and its error logs.
        let asset = AVURLAsset(url: URL(string: "filebeans-media://playback/" + UUID().uuidString)!)
        asset.resourceLoader.setDelegate(self, queue: .main)
        return asset
    }

    func cancel() {
        for task in tasks.values { task.cancel() }
        tasks.removeAll()
    }

    func resourceLoader(_ resourceLoader: AVAssetResourceLoader,
                        shouldWaitForLoadingOfRequestedResource loading: AVAssetResourceLoadingRequest) -> Bool {
        let id = ObjectIdentifier(loading)
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
                try await MediaRange.read(offset: offset, length: length, fetch: { offset, count in
                    try await self.client.streamChunk(self.request, offset: offset, length: count)
                }, receive: { chunk in
                    if let info = loading.contentInformationRequest {
                        info.contentLength = chunk.totalLength
                        info.contentType = chunk.mimeType.flatMap { UTType(mimeType: $0)?.identifier }
                            ?? UTType(filenameExtension: (self.name as NSString).pathExtension)?.identifier
                            ?? UTType.data.identifier
                        info.isByteRangeAccessSupported = true
                        info.isEntireLengthAvailableOnDemand = true
                    }
                    dataRequest?.respond(with: chunk.data)
                })
                try Task.checkCancellation()
                if !loading.isCancelled { loading.finishLoading() }
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
