import XCTest
@testable import myFiles

final class EditableTextTests: XCTestCase {
    func testMarkdownCodeAndExtensionlessContent() throws {
        for value in ["# Notes\n\n**Bold** and café 👩‍💻\n", "{\"enabled\": true}\r\n", "#!/bin/sh\necho hello\n", "", "a\tb\n"] {
            let bytes = Data(value.utf8)
            let content = try EditableText(data: bytes)
            XCTAssertEqual(content.text, value)
            XCTAssertEqual(try content.data(for: value), bytes)
        }
    }

    func testUnicodeEncodingAndMarkersSurviveEdits() throws {
        let formats: [(Data, String.Encoding)] = [
            (Data([0xef, 0xbb, 0xbf]), .utf8),
            (Data([0xff, 0xfe]), .utf16LittleEndian),
            (Data([0xfe, 0xff]), .utf16BigEndian),
            (Data([0xff, 0xfe, 0, 0]), .utf32LittleEndian),
            (Data([0, 0, 0xfe, 0xff]), .utf32BigEndian)
        ]
        for (marker, encoding) in formats {
            let original = "# Notes\r\n旅行\n"
            let edited = original + "✅ Done\n"
            let bytes = marker + (try XCTUnwrap(original.data(using: encoding)))
            let content = try EditableText(data: bytes)
            XCTAssertEqual(content.text, original)
            XCTAssertEqual(try content.data(for: edited), marker + (try XCTUnwrap(edited.data(using: encoding))))
        }
    }

    func testBinaryAndInvalidEncodingsAreRejected() {
        for bytes in [Data([0, 1, 2]), Data([0xff, 0xd8, 0xff]), Data([0xc3, 0x28]), Data("text\0binary".utf8), Data([0xff, 0xfe, 0x61])] {
            XCTAssertThrowsError(try EditableText(data: bytes))
        }
        XCTAssertThrowsError(try EditableText(data: Data(repeating: 65, count: EditableText.maximumBytes + 1)))
        XCTAssertThrowsError(try EditableText(data: Data()).data(for: String(repeating: "a", count: EditableText.maximumBytes + 1)))
    }

    func testExplicitExtensionsAndDotfilesArePreservedForNewFiles() {
        for name in ["notes.md", "config.yaml", "main.swift", ".env", "text.custom", "NOTES.TXT"] {
            XCTAssertEqual(EditableText.filename(name), name)
        }
        XCTAssertEqual(EditableText.filename(" Notes "), "Notes.txt")
    }
}
