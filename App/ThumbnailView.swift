import SwiftUI

@MainActor
enum ThumbnailCache {
    static let images: NSCache<NSString, UIImage> = {
        let cache = NSCache<NSString, UIImage>()
        cache.countLimit = 200
        cache.totalCostLimit = 32 * 1024 * 1024
        return cache
    }()
}

struct ThumbnailView: View {
    let item: StoredItem
    let client: QuantumClient?
    @State private var image: UIImage?
    private var key: String {
        "\(client?.connection.id.uuidString ?? ""):\(item.id):\(item.file.modified ?? "")"
    }
    var body: some View {
        Group {
            if !item.file.isDirectory, let image { Image(uiImage: image).resizable().scaledToFit().clipShape(RoundedRectangle(cornerRadius: 6)) }
            else {
                Image(systemName: client?.connection.companionURL != nil && item.file.isDirectory && item.file.path == "/Trash" ? "trash.fill" : item.file.isDirectory ? "folder.fill" : item.file.isMedia ? "play.rectangle" : "doc")
                    .resizable().scaledToFit().padding(8).foregroundStyle(item.file.isDirectory ? Color.accentColor : .secondary)
            }
        }
        .accessibilityHidden(true)
        .task(id: key) {
            image = nil
            guard !item.file.isDirectory, item.file.hasPreview == true, let client else { return }
            if let cached = ThumbnailCache.images.object(forKey: key as NSString) { image = cached; return }
            do {
                let data = try await client.thumbnail(source: item.source, file: item.file)
                try Task.checkCancellation()
                if let decoded = UIImage(data: data) {
                    ThumbnailCache.images.setObject(decoded, forKey: key as NSString,
                                                     cost: Int(decoded.size.width * decoded.size.height * 4))
                    image = decoded
                }
            } catch { /* File type icon remains available when previews are disabled. */ }
        }
    }
}
