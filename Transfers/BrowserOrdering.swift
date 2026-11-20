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
