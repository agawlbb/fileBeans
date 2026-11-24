import Foundation

struct EditableText {
    static let maximumBytes = 2 * 1024 * 1024
    let text: String
    private let encoding: String.Encoding
    private let marker: Data

    init(data: Data) throws {
        guard data.count <= Self.maximumBytes else { throw CocoaError(.fileReadTooLarge) }
        // Check UTF-32 first: its little-endian marker starts with UTF-16's.
        let formats: [(Data, String.Encoding)] = [
            (Data([0xff, 0xfe, 0x00, 0x00]), .utf32LittleEndian),
            (Data([0x00, 0x00, 0xfe, 0xff]), .utf32BigEndian),
            (Data([0xff, 0xfe]), .utf16LittleEndian),
            (Data([0xfe, 0xff]), .utf16BigEndian),
            (Data([0xef, 0xbb, 0xbf]), .utf8)
        ]
        let format = formats.first { data.starts(with: $0.0) }
        marker = format?.0 ?? Data()
        encoding = format?.1 ?? .utf8
        let body = Data(data.dropFirst(marker.count))
        guard let value = String(data: body, encoding: encoding),
              value.data(using: encoding) == body, Self.isText(value) else {
            throw Self.invalidText
        }
        text = value
    }

    func data(for value: String) throws -> Data {
        guard Self.isText(value), let body = value.data(using: encoding, allowLossyConversion: false) else { throw Self.invalidText }
        var result = marker
        result.append(body)
        guard result.count <= Self.maximumBytes else {
            throw NSError(domain: "fileBeans.Text", code: 2, userInfo: [NSLocalizedDescriptionKey: "Text exceeds the 2 MB editing limit."])
        }
        return result
    }

    static func filename(_ entered: String) -> String {
        let name = entered.trimmingCharacters(in: .whitespacesAndNewlines)
        return name.hasPrefix(".") || !(name as NSString).pathExtension.isEmpty ? name : name + ".txt"
    }

    private static func isText(_ value: String) -> Bool {
        !value.unicodeScalars.contains {
            ($0.value < 32 && !(9...13).contains($0.value)) || (127...159).contains($0.value) && $0.value != 133
        }
    }

    private static var invalidText: NSError {
        NSError(domain: "fileBeans.Text", code: 1, userInfo: [NSLocalizedDescriptionKey: "This file is not supported plain text."])
    }
}
