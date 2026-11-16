import XCTest
@testable import myFiles

final class FolderBreadcrumbTests: XCTestCase {
    private let connection = Connection(name: "Synthetic", url: URL(string: "https://example.invalid")!)
    private func folder(_ name: String, path: String) -> SourceDestination {
        let item = StoredItem(id: path, parent: "root", source: "VPS",
                              file: RemoteFile(name: name, path: path, type: "directory"),
                              permissions: Permissions(), isSource: path == "/")
        return SourceDestination(connection: connection, kind: .local(item))
    }
    func testJumpToSourcePreservesSourceAndRemovesDescendants() {
        let path = [folder("VPS", path: "/"), folder("Photos", path: "/Photos"), folder("2026", path: "/Photos/2026")]
        XCTAssertEqual(FolderBreadcrumbContext.prefix(path, through: 0), [path[0]])
        XCTAssertEqual(FolderBreadcrumbContext.prefix(path, through: 1), Array(path.prefix(2)))
        XCTAssertEqual(FolderBreadcrumbContext.prefix(path, through: 2), path)
    }
    func testInvalidAncestorDoesNotRedirectToAnUnrelatedFolder() {
        let path = [folder("VPS", path: "/")]
        XCTAssertEqual(FolderBreadcrumbContext.prefix(path, through: -1), path)
        XCTAssertEqual(FolderBreadcrumbContext.prefix(path, through: 9), path)
        XCTAssertTrue(FolderBreadcrumbContext.prefix([], through: 0).isEmpty)
    }
    func testDriveHierarchyKeepsOpaqueIDsAndDuplicateNamesDistinct() {
        let root = SourceDestination(connection: connection, kind: .drive)
        let first = SourceDestination(connection: connection, kind: .driveFolder("opaque-one", "Photos"))
        let second = SourceDestination(connection: connection, kind: .driveFolder("opaque-two", "Photos"))
        XCTAssertNotEqual(first.id, second.id)
        XCTAssertEqual(FolderBreadcrumbContext.prefix([root, first, second], through: 1), [root, first])
        XCTAssertEqual(first.name, "Photos")
    }
}
