import SwiftUI

struct ThumbnailView: View {
    let item: StoredItem
    let client: FilesystemClient?
    @State private var image: UIImage?
    @State private var imageKey: ThumbnailKey?
    private var canPreview: Bool {
        item.protection?.unlocked != false && !item.file.isDirectory && item.file.hasPreview == true && client != nil
    }
    private var displayedImage: UIImage? {
        guard canPreview else { return nil }
        return (imageKey == key ? image : nil) ?? ThumbnailCache.shared.cachedImage(for: key)
    }
    private var key: ThumbnailKey {
        ThumbnailKey(connection: client?.connection.id ?? UUID(uuidString: "00000000-0000-0000-0000-000000000000")!, provider: item.file.type.hasPrefix("video/") ? "filesystem-video-v2" : "filesystem", source: item.source,
                     file: item.id, revision: item.file.modified.map { $0 + ":" + String(item.file.size ?? 0) } ?? "")
    }
    var body: some View {
        Group {
            if let image = displayedImage { Image(uiImage: image).resizable().scaledToFit().clipShape(RoundedRectangle(cornerRadius: 6)) }
            else {
                Image(systemName: client?.connection.companionURL != nil && item.file.isDirectory && item.file.path == "/Trash" ? "trash.fill" : item.file.isDirectory ? "folder.fill" : item.file.isMedia ? "play.rectangle" : "doc")
                    .resizable().scaledToFit().padding(8).foregroundStyle(item.file.isDirectory ? Color.accentColor : .secondary)
            }
        }
        .accessibilityHidden(true)
        .task(id: ThumbnailLoadID(key: key, enabled: canPreview)) {
            guard canPreview, let client else { image = nil; imageKey = nil; return }
            if imageKey == key, image != nil { return }
            do {
                let thumbnail = try await ThumbnailCache.shared.image(for: key) {
                    try await client.thumbnail(source: item.source, file: item.file)
                }
                try Task.checkCancellation()
                image = thumbnail
                imageKey = key
            } catch { /* File type icon remains available when previews are disabled. */ }
        }
    }
}

struct ThumbnailLoadID: Hashable {
    let key: ThumbnailKey
    let enabled: Bool
}
