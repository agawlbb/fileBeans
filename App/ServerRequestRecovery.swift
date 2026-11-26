import UIKit

enum ServerRequestRecovery {
    @MainActor static func waitUntilActive() async throws {
        while UIApplication.shared.applicationState != .active {
            try await Task.sleep(for: .milliseconds(200))
        }
        try Task.checkCancellation()
    }
}
