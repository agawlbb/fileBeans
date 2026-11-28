import XCTest
@testable import myFiles

@MainActor
final class SourcesTests: XCTestCase {
    func testEachConfiguredServerContributesExactlyOneSource() {
        let first = Connection(name: "First", url: URL(string: "https://one.example")!)
        let second = Connection(name: "Second", url: URL(string: "https://two.example")!)
        let root = StoredItem(id: "same-id", parent: "root", source: "VPS",
                              file: RemoteFile(name: "VPS", path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        let entries = SourcesModel.entries(connections: [first, second], roots: [first.id: [root], second.id: [root]])
        XCTAssertEqual(entries.count, 2)
        XCTAssertEqual(Set(entries.map(\.id)).count, 2)
        let local = entries.filter { if case .server = $0.kind { return true }; return false }
        XCTAssertEqual(Set(local.map { $0.connection.id }), Set([first.id, second.id]))
        XCTAssertEqual(entries.filter { if case .drive = $0.kind { return true }; return false }.count, 0)
    }
    func testDriveSourceExistsWithoutAnyServerOrCachedRoots() {
        var drive = Connection(name: "Personal Drive", url: URL(string: "https://www.googleapis.com/drive/v3")!)
        drive.kind = .googleDrive
        let entries = SourcesModel.entries(connections: [drive], roots: [:])
        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries.first?.name, "Personal Drive")
        XCTAssertNil(drive.unified.companionURL)
        if case .drive = entries[0].kind {} else { XCTFail("Drive was represented as a server") }
    }
    func testRemovedConnectionsDoNotContributeCachedRoots() {
        let connection = Connection(name: "Removed", url: URL(string: "https://one.example")!)
        XCTAssertTrue(SourcesModel.entries(connections: [], roots: [connection.id: []]).isEmpty)
    }
    func testServerSourceOpensItsOnlyRootWithoutChangingItsSourceName() {
        let connection = Connection(name: "Pi", url: URL(string: "https://pi.example/companion")!)
        let root = StoredItem(id: "pi-root", parent: "root", source: "Samba",
                              file: RemoteFile(name: "Samba", path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        let entries = SourcesModel.entries(connections: [connection], roots: [connection.id: [root]])
        XCTAssertEqual(entries.first?.name, "Pi")
        XCTAssertEqual(entries.first?.serverRoot, root)
        XCTAssertEqual(entries.first?.connection.id, connection.id)
    }
    func testMultipleRootsRemainSelectableWithinTheirServer() {
        let connection = Connection(name: "Files", url: URL(string: "https://server.example")!)
        let roots = ["Photos", "Documents"].map { name in
            StoredItem(id: name, parent: "root", source: name,
                       file: RemoteFile(name: name, path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        }
        let entries = SourcesModel.entries(connections: [connection], roots: [connection.id: roots])
        XCTAssertEqual(entries.count, 1)
        XCTAssertNil(entries.first?.serverRoot)
    }
    func testLegacyQuantumNameBecomesVPSWithoutChangingConnectionOrCredentials() {
        let credential = UUID()
        let old = Connection(name: "File Browser Quantum", url: URL(string: "https://files.coffeybean.com/quantum")!,
                             companionURL: URL(string: "https://files.coffeybean.com/companion")!, companionTokenID: credential)
        let entry = SourcesModel.entries(connections: [old], roots: [:])[0]
        XCTAssertEqual(entry.name, "VPS")
        XCTAssertEqual(entry.connection.id, old.id)
        XCTAssertEqual(entry.connection.filesystemURL, old.companionURL)
        XCTAssertEqual(entry.connection.filesystemTokenID, credential)
        var custom = old; custom.name = "My server"
        XCTAssertEqual(custom.unified.name, "My server")
    }

}
