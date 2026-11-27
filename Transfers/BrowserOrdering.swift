import SwiftUI

struct BrowserSortValue {
    let id: String
    let name: String
    let folder: Bool
    let size: Int64
    let modified: Date
}
enum BrowserOrdering {
    static func isModified(_ order: String) -> Bool { order == "modified" || order == "modifiedAscending" }
    static func selectingModified(_ order: String) -> String { order == "modified" ? "modifiedAscending" : "modified" }
    static func precedes(_ a: BrowserSortValue, _ b: BrowserSortValue, order: String) -> Bool {
        if a.folder != b.folder { return a.folder }
        if isModified(order), a.modified != b.modified {
            if a.modified == .distantPast { return false }
            if b.modified == .distantPast { return true }
            return order == "modified" ? a.modified > b.modified : a.modified < b.modified
        }
        if order == "size", a.size != b.size { return a.size > b.size }
        let comparison = a.name.localizedStandardCompare(b.name)
        return comparison == .orderedSame ? a.id < b.id : comparison == .orderedAscending
    }
}
