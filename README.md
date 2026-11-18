# fileBeans

A native iPhone and iPad client backed by the fileBeans filesystem API.

This feature branch replaces Quantum. See the [trial API, validation and rollback procedure](docs/FILESYSTEM-API.md). It has not been built for Apple devices or deployed.

Browse folders in list or grid view, preview server-generated thumbnails, copy, move and rename items, create folders, delete files, and upload from Files or Photos. Videos and audio stream through the native player. Each connection is also available as a read/write location in Apple's Files app and document pickers.

The fileBeans service provides durable server-side transfers, ten-day local Trash, Google Drive browsing outside Apple Files, and sequential media normalization into VPS `/pics`. Open **Transfers → Media Normalization** to import the configured Drive library and monitor, cancel or retry work. The worker downloads and finishes one file before fetching the next; stable new VPS media is normalized automatically. See [companion setup](companion/README.md#sequential-vps-media-normalization).

Requires iOS/iPadOS 18+, an HTTPS fileBeans server with filesystem API v1 enabled, and its private API token. Add a connection using the server's base URL, a display name, and its token. Enable fileBeans under **Files → Browse → Edit** if it is not visible automatically. Server permissions still apply.

## Development

The source of truth for the Xcode project is `project.yml`. On a Mac, install [XcodeGen](https://github.com/yonaskolb/XcodeGen), run `xcodegen generate`, and open `myFiles.xcodeproj`. Use the shared `myFiles` scheme. The app and `QuantumFileProvider` extension share API, credentials, and catalog code.

The registered release identifiers are in `Config/Project.xcconfig`. For a different signing team during device development, override them in a gitignored `Config/Local.xcconfig`:

```xcconfig
APPLE_TEAM_ID = YOUR_TEAM_ID
BUNDLE_ID_PREFIX = your.registered.app.identifier
```

Register the main app ID and its `.FileProvider` extension with the same App Group (`group.` followed by the main bundle ID) and shared Keychain group. Use your registered identifiers rather than the example defaults. The extension must be signed and installed with the app for device testing.

This VM is for editing and Go/static checks; Apple SDK compilation, simulator tests, and archives run on GitHub macOS runners when explicitly requested. This replacement branch has not run Apple builds or device acceptance. See [release setup](docs/RELEASING.md) and the [device acceptance checklist](docs/TESTING.md).

For static checks, install `scripts/requirements-validation.txt` in an isolated Python environment and run `python scripts/validate-source.py`. This parses Swift, Python, plists, asset JSON, and workflow/project configuration; it does not replace compilation or XCTest.

## Data and security

Experimental server-side encrypted folders are implemented on the
`feature/encrypted-vaults` branch. See [setup, recovery and acceptance
requirements](docs/PROTECTED-FOLDERS.md). They require explicit server
configuration and isolated encrypted caches; ordinary storage is unchanged.

Connections go directly to your servers. API tokens are stored in the device Keychain and shared with the app's extension. Tokens are never added to API URLs. Native media playback uses authenticated HTTPS byte-range requests through a native resource loader. It joins bounded responses to satisfy the player's requested ranges, including seeking, without downloading a local playback copy. HTTPS is required, API redirects are rejected, and there is no certificate-validation bypass, analytics SDK, or third-party backend.

The shared App Group stores connection names, URLs, source permissions, and file metadata. Previews and uploads use local cache files. The Files framework manages its own downloaded content and pending changes. Remove a connection to unregister its Files domain and delete its saved token and catalog. Temporary files expire through app cleanup and iOS storage management.

Search currently filters the displayed folder. Sorting keeps folders first. Copies can target any accessible source in the same fileBeans connection, including a server-mounted Samba share. Moves across sources or protection boundaries use durable jobs. In fileBeans, use Copy from an item’s context menu or select multiple items and choose Copy, then choose a destination folder. Copies run on the server without downloading and uploading file contents; existing destination names are never overwritten. Apple’s Files app manages its own copy transfers. Remote renames appear as removal/addition because the API currently exposes paths rather than stable object IDs; app-initiated renames and moves retain local identities. Metadata refresh occurs when browsing or when Files requests reconciliation; there is no continuous background polling.

Writeback requires the expected server revision when replacing contents. Stale edits are rejected; arbitrary external filesystem writers can still race a check and publication. Conflict and upload failures are surfaced to Files; its pending local contents are not deleted. Explicit offline folder synchronization, photo backup, SSO, server administration, and media transcoding remain outside this change.

## fileBeans service

The [companion service](companion/README.md) adds persistent background copy jobs,
automatic interrupted-copy recovery, ten-day trash on VPS/Samba storage, and a
Google Drive browser with native file management. Configure its HTTPS URL and API token in
the connection editor. Drive/server copies in both directions and Drive-to-Drive
copies run on the VPS, with resumable uploads and persistent progress. Drive is
visible only inside fileBeans and never registered with Apple Files. Existing
read-only connections require **Upgrade Drive Access** for writes. Account choice
happens at Google sign-in; no account is embedded in the app. Drive supports native
trash/restore, folders, rename, within-Drive moves, Files/Photos uploads, thumbnails
and supported media playback. Google-native documents open in Google's browser;
export/conversion to server files is deferred.

App and Apple Files extension deletions move local
items to trash; service failures never fall back to permanent deletion. Restore,
Delete Permanently, and Empty Trash are available in fileBeans. Each source has a separate bottom Trash shortcut
at its top level, with no combined Trash source or inline managed trash folder.
Local retention is ten days; Drive uses Google's native 30-day policy and includes
items deleted outside fileBeans. Server installation, database migration, Google
consent, and iOS release are separate steps; see the companion instructions.

## Public repository

Keep API tokens, private server addresses, user files, certificates, profiles, and App Store Connect keys out of commits, issues, screenshots, and workflow logs. Examples and tests use synthetic data. Signing secrets belong in GitHub's `testflight` environment. No license grant has been selected yet.
