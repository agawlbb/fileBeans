import Foundation

enum SourceTransfers {
    /// Chooses the explicitly configured FileBeans endpoint that owns the local
    /// side. Google credentials are sealed to that endpoint for this job only.
    static func submit(origin: Connection, destination: Connection, copy: BackgroundCopy) async throws -> CopyJob {
        if origin.id == destination.id {
            let client = CompanionClient(connection: origin); defer { client.close() }
            return try await client.submit(copy)
        }
        guard !(origin.isGoogleDrive && destination.isGoogleDrive) else {
            throw NSError(domain: "fileBeans.Transfer", code: 409,
                          userInfo: [NSLocalizedDescriptionKey: "Transfers between different Google accounts need a FileBeans transfer server."])
        }
        let owner = origin.isGoogleDrive ? destination : origin
        let client = CompanionClient(connection: owner); defer { client.close() }
        var request = copy
        let drive = origin.isGoogleDrive ? origin : destination.isGoogleDrive ? destination : nil
        if !destination.isGoogleDrive && destination.id != owner.id {
            let remote = CompanionClient(connection: destination); defer { remote.close() }
            let capabilities = try await remote.capabilities()
            guard let identity = capabilities.serverId, !identity.isEmpty else { throw APIError.incompatible }
            let scope = try SharedStore().sourceScope(destination.id, source: request.destination.source)
            request.destination.path = try FilePath.inSource(request.destination.path, scope: scope)
            request.destination.server = identity
        }
        let job = try await client.submit(request, googleDrive: drive)
        return try ClientTransferLinks.register(job, origin: origin, owner: owner)
    }
}
