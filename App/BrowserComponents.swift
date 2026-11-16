import SwiftUI

struct BrowserSortValue {
    let id: String
    let name: String
    let folder: Bool
    let size: Int64
    let modified: Date
}
enum BrowserOrdering {
    static func precedes(_ a: BrowserSortValue, _ b: BrowserSortValue, order: String) -> Bool {
        if a.folder != b.folder { return a.folder }
        if order == "modified", a.modified != b.modified { return a.modified > b.modified }
        if order == "size", a.size != b.size { return a.size > b.size }
        let comparison = a.name.localizedStandardCompare(b.name)
        return comparison == .orderedSame ? a.id < b.id : comparison == .orderedAscending
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
    var body: some View {
        let layout = large ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 12))
        layout {
            thumbnail.frame(width: large ? 120 : 44, height: large ? 100 : 44)
            VStack(alignment: .leading, spacing: 4) {
                Text(name).foregroundStyle(.primary).lineLimit(2)
                if !folder, let size { Text(ByteCountFormatter.string(fromByteCount: size, countStyle: .file)).font(.caption).foregroundStyle(.secondary) }
            }
            if selecting { Image(systemName: selected ? "checkmark.circle.fill" : "circle").foregroundStyle(.tint) }
        }
        .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
struct BrowserViewOptions: View {
    @Binding var grid: Bool
    @Binding var sortOrder: String
    let select: () -> Void
    let refresh: () -> Void
    var body: some View {
        Menu {
            Button(grid ? "List" : "Grid", systemImage: grid ? "list.bullet" : "square.grid.2x2") { grid.toggle() }
            Picker("Sort", selection: $sortOrder) { Text("Name").tag("name"); Text("Modified").tag("modified"); Text("Size").tag("size") }
            Button("Select", systemImage: "checkmark.circle", action: select)
            Button("Refresh", systemImage: "arrow.clockwise", action: refresh)
        } label: { Label("View Options", systemImage: "ellipsis.circle") }
    }
}
struct BrowserSelectionControls: View {
    let canCopy: Bool
    let canMove: Bool
    let canDelete: Bool
    let copy: () -> Void
    let move: () -> Void
    let delete: () -> Void
    let done: () -> Void
    var body: some View {
        HStack {
            Button("Copy", systemImage: "doc.on.doc", action: copy).disabled(!canCopy)
            Button("Move", systemImage: "folder", action: move).disabled(!canMove)
            Button("Delete", systemImage: "trash", role: .destructive, action: delete).disabled(!canDelete)
            Button("Done", action: done)
        }.labelStyle(.iconOnly)
    }
}
struct SourceTrashBar<Destination: View>: View {
    let destination: Destination
    var body: some View {
        NavigationLink { destination } label: {
            HStack { Label("Trash", systemImage: "trash"); Spacer(); Image(systemName: "chevron.right").font(.caption) }
                .padding()
        }.buttonStyle(.plain).background(.regularMaterial)
    }
}
