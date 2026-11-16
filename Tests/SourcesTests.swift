import XCTest
@testable import myFiles

@MainActor
final class SourcesTests: XCTestCase {
    func testFlattenedSourcesRetainConnectionAndUniqueIdentity() {
        let first = Connection(name: "First", url: URL(string: "https://one.example")!)
        let second = Connection(name: "Second", url: URL(string: "https://two.example")!)
        let root = StoredItem(id: "same-id", parent: "root", source: "VPS",
                              file: RemoteFile(name: "VPS", path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        let entries = SourcesModel.entries(connections: [first, second], roots: [first.id: [root], second.id: [root]])
        XCTAssertEqual(entries.count, 4)
        XCTAssertEqual(Set(entries.map(\.id)).count, 4)
        let local = entries.filter { $0.name == "VPS" }
        XCTAssertEqual(Set(local.map { $0.connection.id }), Set([first.id, second.id]))
        XCTAssertEqual(entries.filter { $0.name == "Google Drive" }.count, 2)
    }
    func testRemovedConnectionsDoNotContributeCachedRoots() {
        let connection = Connection(name: "Removed", url: URL(string: "https://one.example")!)
        XCTAssertTrue(SourcesModel.entries(connections: [], roots: [connection.id: []]).isEmpty)
    }
}
