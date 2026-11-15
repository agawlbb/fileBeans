import FileProvider
import UniformTypeIdentifiers
import CryptoKit

final class ProviderItem: NSObject, NSFileProviderItem {
    let stored: StoredItem
    init(_ stored: StoredItem) { self.stored = stored }
    var itemIdentifier: NSFileProviderItemIdentifier { .init(stored.id) }
    var parentItemIdentifier: NSFileProviderItemIdentifier {
        stored.parent == "root" ? .rootContainer : .init(stored.parent)
    }
    var filename: String { stored.file.name }
    var contentType: UTType {
        if stored.file.isDirectory { return .folder }
        return UTType(mimeType: stored.file.type) ?? UTType(filenameExtension: (filename as NSString).pathExtension) ?? .data
    }
    var typeIdentifier: String { contentType.identifier }
    var documentSize: NSNumber? { stored.file.size.map { NSNumber(value: $0) } }
    var contentModificationDate: Date? { stored.file.date }
    var itemVersion: NSFileProviderItemVersion {
        let p = stored.permissions
        let metadata = "\(stored.parent):\(stored.file.path):\(filename):\(stored.file.type):\(p.view):\(p.download):\(p.modify):\(p.create):\(p.delete)"
        return .init(contentVersion: stored.file.version, metadataVersion: Data(SHA256.hash(data: Data(metadata.utf8))))
    }
    var capabilities: NSFileProviderItemCapabilities {
        var result: NSFileProviderItemCapabilities = []
        if stored.permissions.download { result.formUnion([.allowsReading, .allowsEvicting]) }
        if stored.file.isDirectory && stored.permissions.view { result.formUnion([.allowsReading, .allowsContentEnumerating]) }
        if stored.file.isDirectory && stored.permissions.create { result.insert(.allowsAddingSubItems) }
        if !stored.isSource {
            if stored.permissions.modify { result.formUnion([.allowsRenaming, .allowsReparenting]); if !stored.file.isDirectory { result.insert(.allowsWriting) } }
            if stored.permissions.delete { result.insert(.allowsDeleting) }
        }
        return result
    }
}

func providerError(_ error: Error) -> Error {
    guard let api = error as? APIError else { return error }
    switch api {
    case .authentication: return NSFileProviderError(.notAuthenticated)
    case .missing: return NSFileProviderError(.noSuchItem)
    case .conflict:
        // The version-specific File Provider error is macOS-only. Keep the iOS
        // operation pending and explain the conflict without replacing local edits.
        return CocoaError(.fileWriteUnknown, userInfo: [NSLocalizedDescriptionKey: api.localizedDescription])
    case .forbidden: return CocoaError(.fileWriteNoPermission)
    default: return error
    }
}
