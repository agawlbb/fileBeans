import XCTest
@testable import myFiles

final class ProtectionGroupTests: XCTestCase {
    private let payload = #"{"state":"unlocked","group":"pair","groupState":"partial","members":[{"server":"one","name":"VPS","state":"unlocked"},{"server":"two","name":"Pi","state":"unavailable","error":"Unavailable"}]}"#
    func testMixedGroupOffersLockAndUnlock() throws {
        let value = try JSONDecoder().decode(ProtectionStatus.self, from: Data(payload.utf8))
        XCTAssertTrue(value.hasUnlocked); XCTAssertTrue(value.needsUnlock)
        XCTAssertEqual(value.members?.last?.error, "Unavailable")
    }
    func testLegacyStatusRemainsCompatible() throws {
        let value = try JSONDecoder().decode(ProtectionStatus.self, from: Data(#"{"state":"locked"}"#.utf8))
        XCTAssertFalse(value.hasUnlocked); XCTAssertTrue(value.needsUnlock); XCTAssertNil(value.group)
    }
    func testTwoSourcesProduceOneProtectionControl() throws {
        let value = try JSONDecoder().decode(ProtectionStatus.self, from: Data(payload.utf8))
        let a = Connection(name: "VPS", url: URL(string: "https://one.example")!, serverID: "one")
        let b = Connection(name: "Pi", url: URL(string: "https://two.example")!, serverID: "two")
        let groups = ProtectionControlGroup.groups([a,b], statuses: [a.id:value], reachable: [a.id])
        XCTAssertEqual(groups.count, 1); XCTAssertEqual(groups[0].connections.count, 2)
    }
    func testReachablePeerRepresentsGroupWhenOtherConnectionIsOffline() throws {
        let value = try JSONDecoder().decode(ProtectionStatus.self, from: Data(payload.utf8))
        let a = Connection(name: "VPS", url: URL(string: "https://one.example")!, serverID: "one")
        let b = Connection(name: "Pi", url: URL(string: "https://two.example")!, serverID: "two")
        let groups = ProtectionControlGroup.groups([a,b], statuses: [a.id:value,b.id:value], reachable: [b.id])
        XCTAssertEqual(groups[0].connections.first?.id, b.id)
    }
    func testIndependentServerKeepsSeparateControl() throws {
        let value = try JSONDecoder().decode(ProtectionStatus.self, from: Data(payload.utf8))
        let a = Connection(name: "VPS", url: URL(string: "https://one.example")!, serverID: "one")
        let b = Connection(name: "Other", url: URL(string: "https://other.example")!, serverID: "other")
        let groups = ProtectionControlGroup.groups([a,b], statuses: [a.id:value,b.id:ProtectionStatus(state:"locked")], reachable: [a.id,b.id])
        XCTAssertEqual(groups.count, 2)
    }
}
