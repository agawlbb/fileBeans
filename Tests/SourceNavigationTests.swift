import XCTest
import SwiftUI
@testable import myFiles

@MainActor
private final class NavigationFixture: ObservableObject {
    @Published var path: [SourceDestination] = []
    @Published var selected: SourceDestination?
    var loaded: [String] = []
    var loadFinished: XCTestExpectation?
    var cancelled: XCTestExpectation?
    var navigate: ((Int) -> Void)?

    func load(_ source: SourceDestination) async {
        do {
            // Model a suspended network listing owned by a visible destination.
            try await Task.sleep(for: .milliseconds(50))
            try Task.checkCancellation()
            loaded.append(source.name)
            loadFinished?.fulfill()
            try await Task.sleep(for: .seconds(3600))
        } catch {
            if Task.isCancelled { cancelled?.fulfill() }
        }
    }
}

private struct BreadcrumbProbe: View {
    let fixture: NavigationFixture
    @Environment(\.folderBreadcrumbs) private var trail
    var body: some View {
        Text("Sources").onAppear { fixture.navigate = trail.navigate }
    }
}

private struct NavigationHarness: View {
    @ObservedObject var fixture: NavigationFixture
    var compact = true
    @State private var visibility: NavigationSplitViewVisibility = .all
    var body: some View {
        SourceNavigation(compact: compact, path: $fixture.path, selected: $fixture.selected,
                         columnVisibility: $visibility, sidebar: BreadcrumbProbe(fixture: fixture)) { source in
            Text(source.name).task { await fixture.load(source) }
        }
    }
}

@MainActor
final class SourceNavigationTests: XCTestCase {
    func testIPadTappingCurrentSidebarPathReplacesTheDetailStack() async throws {
        let fixture = NavigationFixture()
        let connection = Connection(name: "Synthetic", url: URL(string: "https://example.invalid")!)
        let item = StoredItem(id: "disk", parent: "root", source: "disk",
            file: RemoteFile(name: "Disk", path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        fixture.selected = SourceDestination(connection: connection, kind: .local(item))
        let controller = UIHostingController(rootView: NavigationHarness(fixture: fixture, compact: false))
        controller.traitOverrides.horizontalSizeClass = .regular
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = controller
        let initial = expectation(description: "Initial folder loads")
        fixture.loadFinished = initial
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        await fulfillment(of: [initial], timeout: 5)
        let reopened = expectation(description: "Current path recreates folder detail")
        fixture.loadFinished = reopened
        try XCTUnwrap(fixture.navigate)(0)
        await fulfillment(of: [reopened], timeout: 5)
        XCTAssertEqual(fixture.loaded, ["Disk", "Disk"])
    }

    func testBackThenDifferentSourceLoads() async {
        await checkReopening(names: ["Samba", "VPS", "Google Drive"])
    }

    func testBackThenSameSourceLoadsAgain() async {
        await checkReopening(names: ["VPS", "VPS", "VPS"])
    }

    func testIPadCanLeaveProtectionThroughSidebarSelection() async {
        let fixture = NavigationFixture()
        let controller = UIHostingController(rootView: NavigationHarness(fixture: fixture, compact: false))
        controller.traitOverrides.horizontalSizeClass = .regular
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 1024, height: 768))
        window.rootViewController = controller
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        let connection = Connection(name: "Synthetic", url: URL(string: "https://example.invalid")!)
        let source = StoredItem(id: "samba", parent: "root", source: "Samba",
            file: RemoteFile(name: "Samba", path: "/", type: "directory"), permissions: Permissions(), isSource: true)
        let destinations = [
            SourceDestination(connection: connection, kind: .protection),
            SourceDestination(connection: connection, kind: .local(source)),
            SourceDestination(connection: connection, kind: .protection),
            SourceDestination(connection: connection, kind: .transfers)
        ]
        for destination in destinations {
            let loaded = expectation(description: destination.name + " loads after sidebar selection")
            fixture.loadFinished = loaded
            fixture.selected = destination
            await fulfillment(of: [loaded], timeout: 5)
        }
        XCTAssertEqual(fixture.loaded, ["Protected folders", "Samba", "Protected folders", "Transfers"])
        let cancelled = expectation(description: "Sources exits protection detail without restarting")
        fixture.cancelled = cancelled
        fixture.selected = nil
        await fulfillment(of: [cancelled], timeout: 5)
    }

    private func checkReopening(names: [String]) async {
        let fixture = NavigationFixture()
        let controller = UIHostingController(rootView: NavigationHarness(fixture: fixture))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 844))
        window.rootViewController = controller
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        let connection = Connection(name: "Synthetic", url: URL(string: "https://example.invalid")!)
        for name in names {
            let item = StoredItem(id: name, parent: "root", source: name,
                                  file: RemoteFile(name: name, path: "/", type: "directory"),
                                  permissions: Permissions(), isSource: true)
            let loaded = expectation(description: "\(name) listing completes after push")
            fixture.loadFinished = loaded
            fixture.path = [SourceDestination(connection: connection, kind: .local(item))]
            await fulfillment(of: [loaded], timeout: 5)
            let cancelled = expectation(description: "Popping \(name) cancels only its task")
            fixture.cancelled = cancelled
            fixture.path = []
            await fulfillment(of: [cancelled], timeout: 5)
            fixture.cancelled = nil
        }
        XCTAssertEqual(fixture.loaded, names)
    }
}
