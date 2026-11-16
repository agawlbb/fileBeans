import SwiftUI

struct FolderBreadcrumbContext {
    var locations: [SourceDestination] = []
    var navigate: (Int) -> Void = { _ in }
    var showSources: () -> Void = {}

    static func prefix(_ path: [SourceDestination], through index: Int) -> [SourceDestination] {
        guard path.indices.contains(index) else { return path }
        return Array(path.prefix(index + 1))
    }
}

private struct FolderBreadcrumbKey: EnvironmentKey {
    static let defaultValue = FolderBreadcrumbContext()
}
extension EnvironmentValues {
    var folderBreadcrumbs: FolderBreadcrumbContext {
        get { self[FolderBreadcrumbKey.self] }
        set { self[FolderBreadcrumbKey.self] = newValue }
    }
}

struct FolderBreadcrumbHeading: View {
    let fallbackTitle: String
    @Environment(\.folderBreadcrumbs) private var trail
    var body: some View {
        if let root = trail.locations.first {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) {
                    ForEach(trail.locations.indices, id: \.self) { index in
                        if index > 0 { separator }
                        crumb(index)
                    }
                }.fixedSize(horizontal: true, vertical: false)
                HStack(spacing: 6) {
                    crumb(0).frame(minWidth: 44, maxWidth: 160).layoutPriority(1)
                    if trail.locations.count > 1 {
                        hierarchyMenu { Image(systemName: "ellipsis") }
                        separator
                        Text(trail.locations.last?.name ?? "")
                            .lineLimit(1).truncationMode(.middle)
                    }
                }
                // Even at very narrow widths the source remains visible and
                // the menu provides every ancestor, including Sources.
                hierarchyMenu { Text(root.name).lineLimit(1).minimumScaleFactor(0.6) }
            }
            .font(.largeTitle.weight(.bold))
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 20)
            .padding(.vertical, 8)
            .background(.regularMaterial)
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Folder path")
            .accessibilityAddTraits(.isHeader)
        } else {
            Text(fallbackTitle)
                .font(.largeTitle.bold())
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 20)
                .padding(.vertical, 8)
                .accessibilityAddTraits(.isHeader)
        }
    }
    private var separator: some View {
        Image(systemName: "chevron.right").font(.title3).foregroundStyle(.secondary)
            .accessibilityHidden(true)
    }
    private func crumb(_ index: Int) -> some View {
        Button { trail.navigate(index) } label: {
            Text(trail.locations[index].name).lineLimit(1).truncationMode(.middle)
                .fontWeight(index == trail.locations.count - 1 ? .bold : .semibold)
                .frame(minHeight: 44)
        }
        .buttonStyle(.plain)
        .foregroundStyle(index == trail.locations.count - 1 ? Color.primary : Color.accentColor)
        .accessibilityLabel("Go to \(trail.locations[index].name)")
    }
    private func hierarchyMenu<Label: View>(@ViewBuilder label: () -> Label) -> some View {
        Menu {
            Button("Sources", systemImage: "externaldrive") { trail.showSources() }
            ForEach(trail.locations.indices, id: \.self) { index in
                Button(trail.locations[index].name, systemImage: index == 0 ? "externaldrive" : "folder") {
                    trail.navigate(index)
                }
            }
        } label: { label().frame(minHeight: 44) }
        .accessibilityLabel("Full folder path")
    }
}

struct FolderBreadcrumbSidebar: View {
    @Environment(\.folderBreadcrumbs) private var trail
    var body: some View {
        if !trail.locations.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                Text("Current folder").font(.caption).foregroundStyle(.secondary)
                ForEach(trail.locations.indices, id: \.self) { index in
                    Button { trail.navigate(index) } label: {
                        Label(trail.locations[index].name, systemImage: index == 0 ? "externaldrive" : "folder")
                            .fontWeight(index == trail.locations.count - 1 ? .semibold : .regular)
                            .lineLimit(2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.leading, CGFloat(min(index, 6)) * 12)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(index == trail.locations.count - 1 ? Color.primary : Color.accentColor)
                    .accessibilityLabel("Go to \(trail.locations[index].name)")
                }
                Divider()
            }
            .padding(.vertical, 8)
        }
    }
}
