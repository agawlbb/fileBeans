import XCTest
import SwiftUI
@testable import myFiles

@MainActor
private final class NavigationFixture: ObservableObject {
    @Published var path: [SourceDestination] = []
    var loaded: [String] = []
    var loadFinished: XCTestExpectation?
    var cancelled: XCTestExpectation?

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

private struct NavigationHarness: View {
    @ObservedObject var fixture: NavigationFixture
    @State private var selected: SourceDestination?
    @State private var visibility: NavigationSplitViewVisibility = .all
    var body: some View {
        SourceNavigation(compact: true, path: $fixture.path, selected: $selected,
                         columnVisibility: $visibility, sidebar: Text("Sources")) { source in
            Text(source.name).task { await fixture.load(source) }
        }
    }
}

@MainActor
final class SourceNavigationTests: XCTestCase {
    func testBackThenDifferentSourceLoads() async {
        await checkReopening(names: ["Samba", "VPS", "Google Drive"])
    }

    func testBackThenSameSourceLoadsAgain() async {
        await checkReopening(names: ["VPS", "VPS", "VPS"])
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
