import XCTest

final class FolderViewerGestureTests: XCTestCase {
    func testVideoFullScreenHasOneControlAndKeepsDecoding() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--folder-viewer-gesture-test", "--folder-viewer-video-test"]
        app.launch()
        app.buttons["Open Viewer"].tap()
        let probe = app.staticTexts["Playback Probe"]
        XCTAssertTrue(probe.waitForExistence(timeout: 15))
        let started = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            self.playbackValue("frames", probe: probe).flatMap(Int.init).map { $0 >= 3 } == true
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [started], timeout: 15), .completed)
        let item = playbackValue("item", probe: probe)
        for _ in 0..<3 {
            let fullScreenControls = app.buttons.matching(NSPredicate(format:
                "label CONTAINS[c] 'full screen' OR label CONTAINS[c] 'fullscreen'"))
            XCTAssertEqual(fullScreenControls.count, 1, "Video must expose only the viewer's full-screen control")
            let before = Int(playbackValue("frames", probe: probe) ?? "0") ?? 0
            app.buttons["Full Screen"].tap()
            let moving = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
                self.playbackValue("attached", probe: probe) == "true" &&
                self.playbackValue("item", probe: probe) == item &&
                (Int(self.playbackValue("frames", probe: probe) ?? "0") ?? 0) >= before + 3 &&
                !app.buttons["Done"].exists
            }, object: nil)
            XCTAssertEqual(XCTWaiter.wait(for: [moving], timeout: 10), .completed,
                           "Full screen must keep the same item attached and decoding: \(probe.label)")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.15, dy: 0.5)).tap()
            XCTAssertTrue(app.buttons["Full Screen"].waitForExistence(timeout: 5))
            XCTAssertEqual(playbackValue("item", probe: probe), item)
            XCTAssertEqual(playbackValue("attached", probe: probe), "true")
        }
        app.buttons["Done"].tap()
        XCTAssertTrue(app.buttons["Open Viewer"].waitForExistence(timeout: 5))
    }

    private func playbackValue(_ key: String, probe: XCUIElement) -> String? {
        probe.label.split(separator: ";").first { $0.hasPrefix(key + "=") }
            .map { String($0.dropFirst(key.count + 1)) }
    }

    func testFullScreenTogglePreservesPagingAndSelection() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--folder-viewer-gesture-test", "--folder-viewer-tall-photo-test"]
        app.launch()
        app.buttons["Open Viewer"].tap()
        assertPhoto("first.png", in: app)
        let normalHeight = app.images["first.png"].frame.height
        app.buttons["Full Screen"].tap()
        assertPhoto("first.png", in: app, controlsVisible: false)
        let expanded = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            app.images["first.png"].frame.height > normalHeight
        }, object: nil)
        let result = XCTWaiter.wait(for: [expanded], timeout: 5)
        if result != .completed { print(app.debugDescription) }
        XCTAssertEqual(result, .completed,
                       "Full screen must expand the media beyond the normal safe area (normal: \(normalHeight), full: \(app.images["first.png"].frame.height))")
        app.swipeLeft()
        assertPhoto("second.png", in: app, controlsVisible: false)
        app.swipeLeft()
        assertPhoto("third.png", in: app, controlsVisible: false)
        app.swipeRight()
        assertPhoto("second.png", in: app, controlsVisible: false)
        app.images["second.png"].tap()
        XCTAssertTrue(app.buttons["Full Screen"].waitForExistence(timeout: 5))
        assertPhoto("second.png", in: app)
        app.swipeRight()
        assertPhoto("first.png", in: app)
        app.buttons["Done"].tap()
        XCTAssertTrue(app.buttons["Open Viewer"].waitForExistence(timeout: 5))
    }

    func testPhotoSwipesNavigateBothDirectionsAndKeepFullScreen() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--folder-viewer-gesture-test"]
        app.launch()
        app.buttons["Open Viewer"].tap()
        assertPhoto("first.png", in: app)
        app.swipeLeft()
        assertPhoto("second.png", in: app)
        app.swipeLeft()
        assertPhoto("third.png", in: app)
        app.swipeLeft()
        assertPhoto("third.png", in: app)
        app.swipeRight()
        assertPhoto("second.png", in: app)
        app.swipeRight()
        assertPhoto("first.png", in: app)
        app.swipeRight()
        assertPhoto("first.png", in: app)
        app.buttons["Done"].tap()
        XCTAssertTrue(app.buttons["Open Viewer"].waitForExistence(timeout: 5))
    }

    func testPhotoCanZoomThenResumeFolderSwiping() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--folder-viewer-gesture-test"]
        app.launch()
        app.buttons["Open Viewer"].tap()
        assertPhoto("first.png", in: app)
        app.images["first.png"].doubleTap()
        app.swipeLeft()
        assertPhoto("first.png", in: app)
        app.images["first.png"].doubleTap()
        app.swipeLeft()
        assertPhoto("second.png", in: app)
    }

    private func assertPhoto(_ name: String, in app: XCUIApplication, controlsVisible: Bool = true, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(app.images[name].waitForExistence(timeout: 10), "Expected visible photo \(name)", file: file, line: line)
        XCTAssertTrue(app.images[name].isHittable, file: file, line: line)
        let controls = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            app.buttons["Done"].exists == controlsVisible &&
            app.buttons["Full Screen"].exists == controlsVisible &&
            !app.buttons["Exit Full Screen"].exists
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [controls], timeout: 5), .completed,
                       "Viewer controls must be hidden in full screen", file: file, line: line)
        XCTAssertFalse(app.buttons["Open Viewer"].isHittable, "Viewer must remain full screen", file: file, line: line)
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
