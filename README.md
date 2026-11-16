# fileBeans

A native iPhone and iPad client for File Browser Quantum v2. An independent project, not affiliated with File Browser Quantum or Google.

Browse folders in list or grid view, preview server-generated thumbnails, copy, move and rename items, create folders, delete files, and upload from Files or Photos. Videos and audio stream through the native player. Each connection is also available as a read/write location in Apple's Files app and document pickers.

Requires iOS/iPadOS 18+, a Quantum v2 HTTPS server, and an API token with API access enabled. Add a connection using the server's base URL, a display name, and its token. Enable fileBeans under **Files → Browse → Edit** if it is not visible automatically. Server permissions still apply.

## Development

The source of truth for the Xcode project is `project.yml`. On a Mac, install [XcodeGen](https://github.com/yonaskolb/XcodeGen), run `xcodegen generate`, and open `myFiles.xcodeproj`. Use the shared `myFiles` scheme. The app and `QuantumFileProvider` extension share API, credentials, and catalog code.

The registered release identifiers are in `Config/Project.xcconfig`. For a different signing team during device development, override them in a gitignored `Config/Local.xcconfig`:

```xcconfig
APPLE_TEAM_ID = YOUR_TEAM_ID
BUNDLE_ID_PREFIX = your.registered.app.identifier
```

Register the main app ID and its `.FileProvider` extension with the same App Group (`group.` followed by the main bundle ID) and shared Keychain group. Use your registered identifiers rather than the example defaults. The extension must be signed and installed with the app for device testing.

This VM is for editing and static checks; Apple SDK compilation, simulator tests, and archives run on GitHub macOS runners. The app and Files extension compile, and all 19 unit tests pass on the simulator. Validate iOS also builds an unsigned device Release archive. Signed-device checks and TestFlight upload remain pending. See [release setup](docs/RELEASING.md) and the [device acceptance checklist](docs/TESTING.md).

For static checks, install `scripts/requirements-validation.txt` in an isolated Python environment and run `python scripts/validate-source.py`. This parses Swift, Python, plists, asset JSON, and workflow/project configuration; it does not replace compilation or XCTest.

## Data and security

Connections go directly to your servers. API tokens are stored in the device Keychain and shared with the app's extension. Tokens are never added to API URLs. Native media playback uses Quantum's scoped view token and authenticated HTTPS byte-range requests through a native resource loader. It joins bounded responses to satisfy the player's requested ranges, including seeking, without downloading a local playback copy. HTTPS is required, API redirects are rejected, and there is no certificate-validation bypass, analytics SDK, or third-party backend.

The shared App Group stores connection names, URLs, source permissions, and file metadata. Previews and uploads use local cache files. The Files framework manages its own downloaded content and pending changes. Remove a connection to unregister its Files domain and delete its saved token and catalog. Temporary files expire through app cleanup and iOS storage management.

Search currently filters the displayed folder. Sorting keeps folders first. Copies can target any accessible source in the same Quantum connection, including a server-mounted Samba share. Moves stay within a server source. In fileBeans, use Copy from an item’s context menu or select multiple items and choose Copy, then choose a destination folder. Copies run on the server without downloading and uploading file contents; existing destination names are never overwritten. Keep the app open while it waits for completion. Apple’s Files app manages its own copy transfers. Remote renames appear as removal/addition because Quantum's API exposes paths rather than stable object IDs; app-initiated renames and moves retain local identities. Metadata refresh occurs when browsing or when Files requests reconciliation; there is no continuous background polling.

Writeback checks server modification time and size before uploading. Quantum does not document an atomic conditional-write API, so a remote edit racing the subsequent write cannot be fully prevented. Conflict and upload failures are surfaced to Files; its pending local contents are not deleted. Explicit offline folder synchronization, photo backup, SSO, cross-source moves, server administration, and media transcoding are outside the initial release.

## Public repository

Keep API tokens, private server addresses, user files, certificates, profiles, and App Store Connect keys out of commits, issues, screenshots, and workflow logs. Examples and tests use synthetic data. Signing secrets belong in GitHub's `testflight` environment. No license grant has been selected yet.
