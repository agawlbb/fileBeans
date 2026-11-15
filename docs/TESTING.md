# Acceptance checks

Use a disposable Quantum v2 instance or test account and synthetic files. Do not test mutations against production files. Run XCTest using the shared `myFiles` scheme on a GitHub macOS runner when a build is authorized.

- Connect to two servers, including one mounted under a URL base path. Restart the app and confirm names and credentials persist. Replace an expired token; remove a connection and confirm its Files location disappears.
- Validate rejected HTTP URLs, invalid tokens, disabled API access, insufficient permissions, and v1-style responses. Ensure UI errors contain no credentials or server response bodies.
- Browse nested folders in both layouts on iPhone and iPad, rotate, use split view, dark mode, VoiceOver, and large Dynamic Type. Test empty folders and missing thumbnails.
- Rename and move a folder with children. Confirm existing Files identities remain usable. Delete selected items after confirmation. Verify name collisions never silently overwrite. Include Unicode, spaces, plus signs, ampersands, and percent signs in filenames.
- Upload from Files and Photos, including a large video. Observe progress, cancel, retry after network loss, and confirm collisions retain the local retry copy. No automatic retry of uncertain destructive operations.
- In Files and another app's document picker, browse, open, create, save edits, rename, move, and delete. Verify read-only accounts expose appropriate capabilities. Test connection loss during save, token revocation, and a remote edit before writeback; pending local changes must survive failures.
- Change and delete items using the Quantum web UI, then refresh the app and Files. Confirm deleted items disappear through change enumeration, including deletions initiated by the app.
- Stream a large supported video, seek near its end, interrupt the connection, and reopen after view-token expiry. Test an unsupported codec and a document without a native preview.
- Inspect logs, Git changes, and packaged resources for secrets and private user data before sharing. Verify signed-device behavior for both extension entitlements before a TestFlight upload.

The app and Files extension passed Apple SDK compilation and all 19 simulator unit tests on [GitHub macOS/Xcode 26.3](https://github.com/agawlbb/myFiles/actions/runs/43733327992). Validate iOS also builds an unsigned Release archive for actual devices. Signed-device checks, Files lifecycle behavior, live-server interoperability, and playback codecs still require the acceptance checks above.
