import Foundation

struct MediaChunk {
    let data: Data
    let totalLength: Int64
    let mimeType: String?

    static func decode(_ data: Data, response: HTTPURLResponse, offset: Int64, length: Int) throws -> MediaChunk {
        let range = try MediaRangeResponse(response, offset: offset, length: length)
        guard data.count == range.length else { throw URLError(.badServerResponse) }
        return MediaChunk(data: data, totalLength: range.total, mimeType: range.mimeType)
    }
}

/// Check headers before any response bytes are supplied to AVPlayer.
struct MediaRangeResponse {
    let length: Int
    let total: Int64
    let mimeType: String?

    init(_ response: HTTPURLResponse, offset: Int64, length: Int) throws {
        guard response.statusCode == 206, offset >= 0, length > 0,
              let header = response.value(forHTTPHeaderField: "Content-Range"), header.hasPrefix("bytes "),
              response.value(forHTTPHeaderField: "Content-Encoding").map({ $0.lowercased() == "identity" }) ?? true else {
            throw URLError(.badServerResponse)
        }
        let parts = header.dropFirst(6).split(separator: "/", omittingEmptySubsequences: false)
        guard parts.count == 2, let total = Int64(parts[1]), total > 0 else { throw URLError(.badServerResponse) }
        let bounds = parts[0].split(separator: "-", omittingEmptySubsequences: false)
        guard bounds.count == 2, let start = Int64(bounds[0]), let end = Int64(bounds[1]),
              start == offset, start >= 0, end >= start, end < total,
              end - start + 1 <= Int64(length) else {
            throw URLError(.badServerResponse)
        }
        self.length = Int(end - start + 1)
        self.total = total
        mimeType = response.mimeType
    }
}

enum MediaRange {
    // Use bounded responses so playback never buffers a complete movie.
    static let chunkSize = 1 << 20

    @MainActor
    static func read(offset: Int64, length: Int64?, firstChunkSize: Int = chunkSize,
                     fetch: (Int64, Int) async throws -> MediaChunk,
                     receive: (MediaChunk) throws -> Void) async throws {
        guard firstChunkSize > 0, firstChunkSize <= chunkSize, offset >= 0, length.map({ $0 > 0 && $0 <= Int64.max - offset }) ?? true else {
            throw URLError(.badURL)
        }
        let requestedEnd = length.map { offset + $0 }
        var cursor = offset
        var total: Int64?
        repeat {
            try Task.checkCancellation()
            let end = min(requestedEnd ?? Int64.max, total ?? Int64.max)
            let count = Int(min(Int64(cursor == offset ? firstChunkSize : chunkSize), end - cursor))
            guard count > 0 else { return }
            let chunk = try await fetch(cursor, count)
            try Task.checkCancellation()
            guard !chunk.data.isEmpty, chunk.data.count <= count,
                  chunk.totalLength > cursor, Int64(chunk.data.count) <= chunk.totalLength - cursor,
                  total == nil || total == chunk.totalLength else { throw URLError(.badServerResponse) }
            total = chunk.totalLength
            try receive(chunk)
            cursor += Int64(chunk.data.count)
        } while cursor < min(requestedEnd ?? Int64.max, total ?? Int64.max)
    }
}
