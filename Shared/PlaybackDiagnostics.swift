import Foundation

/// Player errors can embed authenticated URLs, cookies, and server response text.
/// Expose only numeric codes and fixed labels, never descriptions or arbitrary userInfo.
enum PlaybackDiagnostics {
    static func summary(error: NSError?, mediaStatusCodes: [Int] = []) -> String {
        let labels = [
            "AVFoundationErrorDomain": "AVFoundation",
            NSURLErrorDomain: "URL",
            NSOSStatusErrorDomain: "OSStatus",
            "CoreMediaErrorDomain": "CoreMedia"
        ]
        var details: [String] = []
        var current = error
        // A malformed or cyclic underlying-error chain must not hang the player UI.
        for _ in 0..<8 {
            guard let value = current else { break }
            let label = labels[value.domain] ?? "Player"
            let detail = "\(label) \(value.code)"
            if !details.contains(detail) { details.append(detail) }
            current = value.userInfo[NSUnderlyingErrorKey] as? NSError
        }
        for code in mediaStatusCodes.suffix(4) where code != 0 {
            let detail = "Media status \(code)"
            if !details.contains(detail) { details.append(detail) }
        }
        return details.isEmpty ? "No diagnostic code was provided by the player." : details.joined(separator: "; ")
    }
}
