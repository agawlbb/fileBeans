import SwiftUI

struct ThumbnailView: View {
    let item: StoredItem
    let client: QuantumClient?
    @State private var image: UIImage?
    private var key: ThumbnailKey {
        ThumbnailKey(connection: client?.connection.id ?? UUID(uuidString: "00000000-0000-0000-0000-000000000000")!, provider: "quantum", source: item.source,
                     file: item.id, revision: item.file.modified.map { $0 + ":" + String(item.file.size ?? 0) } ?? "")
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
            do {
                let data = try await ThumbnailCache.shared.data(for: key) {
                    try await client.thumbnail(source: item.source, file: item.file)
                }
                try Task.checkCancellation()
                image = UIImage(data: data)
            } catch { /* File type icon remains available when previews are disabled. */ }
        }
    }
}
