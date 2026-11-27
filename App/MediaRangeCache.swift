import Foundation

/// Session-local video bytes. Consumers share both completed ranges and active
/// transfers; a seek does not have to download a previously read range again.
@MainActor
final class MediaRangeCache {
    typealias Fetch = (Int64, Int) -> AsyncThrowingStream<MediaChunk, Error>
    private struct Segment {
        let offset: Int64
        let chunk: MediaChunk
        var used: UInt64
        var end: Int64 { offset + Int64(chunk.data.count) }
    }
    private final class Flight {
        let id = UUID()
        let offset: Int64
        let length: Int
        var data = Data()
        var metadata: MediaChunk?
        var finished = false
        var error: Error?
        var task: Task<Void, Never>?
        var consumers = Set<UUID>()
        var waiters: [UUID: CheckedContinuation<Void, Error>] = [:]
        init(offset: Int64, length: Int) { self.offset = offset; self.length = length }
        var end: Int64 { offset + Int64(length) }
        func wake() {
            let pending = Array(waiters.values); waiters.removeAll()
            for waiter in pending { waiter.resume() }
        }
    }
    private let limit: Int
    private var segments: [Int64: Segment] = [:]
    private var flights: [UUID: Flight] = [:]
    private var total: Int64?
    private var clock: UInt64 = 0
    private(set) var byteCount = 0

    init(limit: Int = 128 * 1024 * 1024) { self.limit = max(0, limit) }

    func cancel() {
        let active = Array(flights.values); flights.removeAll()
        for flight in active {
            flight.task?.cancel(); flight.error = CancellationError(); flight.finished = true; flight.wake()
        }
    }

    func clear() { cancel(); segments.removeAll(); byteCount = 0; total = nil }

    func read(offset: Int64, length: Int, fetch: @escaping Fetch,
              receive: (MediaChunk) throws -> Void) async throws {
        guard offset >= 0, length > 0, offset <= Int64.max - Int64(length) else { throw URLError(.badURL) }
        var cursor = offset
        let end = offset + Int64(length)
        while cursor < min(end, total ?? end) {
            try Task.checkCancellation()
            if let hit = segments.values.first(where: { $0.offset <= cursor && cursor < $0.end }) {
                clock &+= 1; segments[hit.offset]?.used = clock
                let start = Int(cursor - hit.offset)
                let count = Int(min(hit.end, end) - cursor)
                try receive(MediaChunk(data: hit.chunk.data.subdata(in: start..<start + count),
                                       totalLength: hit.chunk.totalLength, mimeType: hit.chunk.mimeType))
                cursor += Int64(count)
                continue
            }
            let flight: Flight
            if let active = flights.values.first(where: { $0.offset <= cursor && cursor < $0.end }) { flight = active }
            else {
                // Stop before the next cached/in-flight range instead of fetching overlapping bytes.
                let next = (segments.values.map(\.offset) + flights.values.map(\.offset))
                    .filter { $0 > cursor }.min() ?? end
                let count = Int(min(Int64(MediaRange.chunkSize), min(next, min(end, total ?? end)) - cursor))
                flight = start(offset: cursor, length: count, fetch: fetch)
            }
            let advanced = try await consume(flight, offset: cursor, end: end, receive: receive)
            guard advanced > cursor else { throw URLError(.badServerResponse) }
            cursor = advanced
        }
    }

    private func start(offset: Int64, length: Int, fetch: @escaping Fetch) -> Flight {
        let flight = Flight(offset: offset, length: length)
        flights[flight.id] = flight
        flight.task = Task {
            do {
                for try await chunk in fetch(offset, length) {
                    try Task.checkCancellation()
                    guard !chunk.data.isEmpty, chunk.data.count <= length - flight.data.count,
                          chunk.totalLength > offset,
                          Int64(flight.data.count + chunk.data.count) <= chunk.totalLength - offset,
                          self.total == nil || self.total == chunk.totalLength else { throw URLError(.badServerResponse) }
                    self.total = chunk.totalLength
                    flight.metadata = chunk; flight.data.append(chunk.data); flight.wake()
                }
                try Task.checkCancellation()
                guard let metadata = flight.metadata else { throw URLError(.badServerResponse) }
                self.insert(offset: offset, chunk: MediaChunk(data: flight.data, totalLength: metadata.totalLength, mimeType: metadata.mimeType))
            } catch { flight.error = error }
            flight.finished = true
            self.flights.removeValue(forKey: flight.id)
            flight.wake()
        }
        return flight
    }

    private func consume(_ flight: Flight, offset: Int64, end: Int64,
                         receive: (MediaChunk) throws -> Void) async throws -> Int64 {
        let consumer = UUID()
        flight.consumers.insert(consumer)
        defer {
            flight.consumers.remove(consumer)
            if flight.consumers.isEmpty, !flight.finished {
                flight.task?.cancel(); flights.removeValue(forKey: flight.id)
            }
        }
        var cursor = offset
        while true {
            try Task.checkCancellation()
            if let error = flight.error { throw error }
            let available = flight.offset + Int64(flight.data.count)
            if cursor < min(available, end), let metadata = flight.metadata {
                let start = Int(cursor - flight.offset)
                let count = Int(min(available, end) - cursor)
                try receive(MediaChunk(data: flight.data.subdata(in: start..<start + count),
                                       totalLength: metadata.totalLength, mimeType: metadata.mimeType))
                cursor += Int64(count)
            }
            if flight.finished || (cursor >= end && cursor < flight.end) { return cursor }
            let waiter = UUID()
            try await withTaskCancellationHandler {
                try Task.checkCancellation()
                try await withCheckedThrowingContinuation { flight.waiters[waiter] = $0 }
            } onCancel: {
                Task { @MainActor in flight.waiters.removeValue(forKey: waiter)?.resume(throwing: CancellationError()) }
            }
        }
    }

    private func insert(offset: Int64, chunk: MediaChunk) {
        guard chunk.data.count <= limit else { return }
        if let old = segments.removeValue(forKey: offset) { byteCount -= old.chunk.data.count }
        while byteCount + chunk.data.count > limit, let oldest = segments.values.min(by: { $0.used < $1.used }) {
            byteCount -= oldest.chunk.data.count; segments.removeValue(forKey: oldest.offset)
        }
        clock &+= 1; segments[offset] = Segment(offset: offset, chunk: chunk, used: clock)
        byteCount += chunk.data.count
    }
}
