import SwiftUI

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
    var body: some View {
        let layout = large ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 12))
        layout {
            thumbnail.frame(width: large ? 120 : 44, height: large ? 100 : 44)
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
        .accessibilityValue(protection?.accessibility ?? "")
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
            Button("Copy", systemImage: "doc.on.doc", action: copy).disabled(!canCopy)
            Button("Move", systemImage: "folder", action: move).disabled(!canMove)
            Button("Delete", systemImage: "trash", role: .destructive, action: delete).disabled(!canDelete)
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
