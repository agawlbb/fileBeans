import XCTest

final class FolderViewerGestureTests: XCTestCase {
    func testFullScreenTogglePreservesPagingAndSelection() {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["--folder-viewer-gesture-test"]
        app.launch()
        app.buttons["Open Viewer"].tap()
        assertPhoto("first.png", in: app)
        let normalHeight = app.images["first.png"].frame.height
        app.buttons["Full Screen"].tap()
        XCTAssertTrue(app.buttons["Exit Full Screen"].waitForExistence(timeout: 5))
        assertPhoto("first.png", in: app)
        XCTAssertGreaterThan(app.images["first.png"].frame.height, normalHeight,
                             "Full screen must expand the media beyond the normal safe area")
        app.swipeLeft()
        assertPhoto("second.png", in: app)
        XCTAssertTrue(app.buttons["Exit Full Screen"].exists)
        app.buttons["Exit Full Screen"].tap()
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

    private func assertPhoto(_ name: String, in app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(app.images[name].waitForExistence(timeout: 10), "Expected visible photo \(name)", file: file, line: line)
        XCTAssertTrue(app.images[name].isHittable, file: file, line: line)
        XCTAssertTrue(app.buttons["Done"].exists, file: file, line: line)
        XCTAssertFalse(app.buttons["Open Viewer"].isHittable, "Viewer must remain full screen", file: file, line: line)
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
