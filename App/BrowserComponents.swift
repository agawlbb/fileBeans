import SwiftUI
import UIKit

enum RenameText {
    static func selection(for name: String, isDirectory: Bool) -> TextSelection {
        var end = name.endIndex
        if !isDirectory, let dot = name.lastIndex(of: "."),
           dot != name.startIndex, name.index(after: dot) != name.endIndex {
            end = dot
        }
        return TextSelection(range: name.startIndex..<end)
    }
}

struct BrowserFileCollection<Item: Identifiable, Header: View, Row: View>: View {
    let items: [Item]
    let grid: Bool
    let header: Header
    let row: (Item, Bool) -> Row
    var loadMore: (() -> Void)? = nil
    var body: some View {
        Group {
            if grid {
                ScrollView {
                    header.padding(.horizontal)
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 140))], spacing: 24) {
                        ForEach(items) { row($0, true) }
                    }.padding()
                    if let loadMore { Button("Load More", action: loadMore).padding() }
                }
            } else {
                List {
                    header
                    ForEach(items) { row($0, false) }
                    if let loadMore { Button("Load More", action: loadMore) }
                }.listStyle(.plain)
            }
        }
    }
}
struct BrowserItemCell<Thumbnail: View>: View {
    let name: String
    let size: Int64?
    let folder: Bool
    let large: Bool
    let selecting: Bool
    let selected: Bool
    let thumbnail: Thumbnail
    var protection: FolderProtection? = nil
    var pending: Bool = false
    var linked: Bool = false
    var body: some View {
        let layout = large ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 12))
        layout {
            thumbnail.frame(width: large ? 120 : 44, height: large ? 100 : 44)
                .overlay(alignment: .topTrailing) {
                    if linked { Image(systemName: "link").font(.caption.bold()).padding(4).background(.background, in: Circle()) }
                }
                .overlay(alignment: .bottomTrailing) {
                    if folder, let protection {
                        Image(systemName: protection.symbol)
                            .font(large ? .body.weight(.bold) : .caption2.weight(.bold))
                            .foregroundStyle(Color.primary)
                            .padding(large ? 7 : 4)
                            .background(.background, in: Circle())
                            .accessibilityHidden(true)
                    }
                }
            VStack(alignment: .leading, spacing: 4) {
                Text(name).foregroundStyle(.primary).lineLimit(2)
                if !folder, let size { Text(ByteCountFormatter.string(fromByteCount: size, countStyle: .file)).font(.caption).foregroundStyle(.secondary) }
            }
            if selecting { Image(systemName: selected ? "checkmark.circle.fill" : "circle").foregroundStyle(.tint) }
        }
        .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
        .opacity(pending ? 0.45 : 1)
        .overlay(alignment: .topTrailing) { if pending { ProgressView().controlSize(.small) } }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityValue([protection?.accessibility, linked ? "Linked archive" : nil].compactMap { $0 }.joined(separator: ", "))
    }
}
struct BrowserViewOptions: View {
    @Binding var grid: Bool
    @Binding var sortOrder: String
    let select: () -> Void
    let refresh: () -> Void
    var body: some View {
        HStack {
            Button("Select", action: select).labelStyle(.titleOnly)
            Menu {
            Button(grid ? "List" : "Grid", systemImage: grid ? "list.bullet" : "square.grid.2x2") { grid.toggle() }
            Picker("Sort", selection: $sortOrder) { Text("Name").tag("name"); Text("Modified").tag("modified"); Text("Size").tag("size") }
            Button("Refresh", systemImage: "arrow.clockwise", action: refresh)
        } label: { Label("View Options", systemImage: "ellipsis.circle") }
        }
    }
}
struct BrowserSelectionControls: View {
    var host: (() -> Void)? = nil
    let canDownload: Bool
    let download: () -> Void
    let canCopy: Bool
    let canMove: Bool
    let canDelete: Bool
    let copy: () -> Void
    let move: () -> Void
    let delete: () -> Void
    let done: () -> Void
    var selectAll: (() -> Void)? = nil
    var body: some View {
        HStack {
            Button("Download", systemImage: "arrow.down.doc", action: download).disabled(!canDownload)
            Button("Copy", systemImage: "doc.on.doc", action: copy).disabled(!canCopy)
            Button("Move", systemImage: "folder", action: move).disabled(!canMove)
            Menu {
                if let host { Button("Host File", systemImage: "link", action: host) }
                Button("Delete", systemImage: "trash", role: .destructive, action: delete).disabled(!canDelete)
            } label: { Label("More", systemImage: "ellipsis.circle") }
        }.labelStyle(.titleAndIcon).frame(maxWidth: .infinity)
    }
}
struct SourceTrashBar: View {
    let destination: SourceDestination
    var body: some View {
        NavigationLink(value: destination) {
            HStack { Label("Trash", systemImage: "trash"); Spacer(); Image(systemName: "chevron.right").font(.caption) }
                .padding().frame(maxWidth: .infinity).contentShape(Rectangle())
        }.buttonStyle(.plain).background(.regularMaterial)
    }
}

struct DownloadFile {
    let name: String
    let load: () async throws -> URL
}

@MainActor
final class BrowserDownloads: ObservableObject {
    @Published var busy = false
    @Published var urls: [URL] = []
    private var directory: URL?

    deinit {
        if let directory { try? FileManager.default.removeItem(at: directory) }
    }

    func clear() {
        urls = []
        if let directory { try? FileManager.default.removeItem(at: directory) }
        directory = nil
    }

    func prepare(_ files: [DownloadFile]) async throws {
        guard !busy, urls.isEmpty, !files.isEmpty else { return }
        busy = true
        defer { busy = false }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        directory = root
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            var prepared: [URL] = []
            for (index, file) in files.enumerated() {
                try Task.checkCancellation()
                let source = try await file.load()
                defer { try? FileManager.default.removeItem(at: source) }
                // Separate directories preserve duplicate names from different sources.
                let folder = root.appendingPathComponent(String(index))
                try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
                let name = (file.name as NSString).lastPathComponent
                guard !name.isEmpty, name != ".", name != ".." else { throw CocoaError(.fileWriteInvalidFileName) }
                let target = folder.appendingPathComponent(name)
                try FileManager.default.moveItem(at: source, to: target)
                prepared.append(target)
            }
            urls = prepared
        } catch { clear(); throw error }
    }
}

private struct DownloadDestinationPicker: UIViewControllerRepresentable {
    let urls: [URL]
    let finish: () -> Void
    func makeCoordinator() -> Coordinator { Coordinator(finish: finish) }
    func makeUIViewController(context: Context) -> UIDocumentPickerViewController {
        let picker = UIDocumentPickerViewController(forExporting: urls, asCopy: true)
        picker.delegate = context.coordinator
        return picker
    }
    func updateUIViewController(_ controller: UIDocumentPickerViewController, context: Context) {}
    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        let finish: () -> Void
        init(finish: @escaping () -> Void) { self.finish = finish }
        func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) { finish() }
        func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) { finish() }
    }
}

private struct BrowserDownloadPresentation: ViewModifier {
    @ObservedObject var downloads: BrowserDownloads
    func body(content: Content) -> some View {
        content
            .sheet(isPresented: Binding(get: { !downloads.urls.isEmpty }, set: { if !$0 { downloads.clear() } }),
                   onDismiss: { downloads.clear() }) {
                DownloadDestinationPicker(urls: downloads.urls) { downloads.clear() }
            }
            .overlay {
                if downloads.busy {
                    ProgressView("Downloading…").padding().background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
                }
            }
    }
}

extension View {
    func browserDownloads(_ downloads: BrowserDownloads) -> some View {
        modifier(BrowserDownloadPresentation(downloads: downloads))
    }
}
