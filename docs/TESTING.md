# Acceptance checks

## Media normalization

- In **Transfers → Media Normalization**, verify the configured VPS library and sequential status. Import the configured Drive folder; repeat the request and confirm completed files are deduplicated.
- Observe download, conversion, validation and publication phases. Cancel during each phase and retry after restart or connection loss. Verify Drive originals remain unchanged and a failed local conversion retains its source.
- Play and seek the normalized pilot video on an iPad. Compare image dimensions, orientation, color, audio and perceived video quality with the original before committing to the full library conversion.
- Add a local VPS file and wait for the stability interval. Confirm automatic conversion, no output loop, safe name conflicts and unchanged unsupported inputs. Trash/delete a normalized output and confirm the scanner does not recreate it.
- The isolated Go/MariaDB race suite covers the sequential pipeline, source checksums, local publication recovery, cancellation, disk admission, Google locking and backup publication fencing. The iOS suite includes native HEVC Main10 playback advancement and seeking through the authenticated resource loader.

Source `7ebd703` passed all **50** simulator tests and the unsigned iPhone/iPad
Release archive on [validation run 43912220792](https://github.com/agawlbb/fileBeans/actions/runs/43912220792).
The HEVC playback/seek test passed. The final Go race/MariaDB suite, real FFmpeg
conversion tests and 22 isolated backup policy tests passed. The deployed worker
has no Samba mount; its backup inventory audit passed. Physical iPad quality
comparison remains part of device acceptance.

## Source home, thumbnail cache, and video full screen

- Cold-launch with one connection: Drive, Samba, and VPS should be available
  immediately without choosing a connection. Check list/grid, search, and the
  persistent iPad source sidebar. On iPhone, open a source, browse nested folders,
  then return to the source list.
- In Settings, add a second connection with duplicate source names. Confirm one
  flat source list with connection subtitles and correct credentials for each
  source. Edit and remove connections while browsing; check navigation resets,
  stale sources disappear, and removing the last connection shows setup guidance.
  Disconnect one server and verify other sources remain usable with a retry for
  the failed connection. Confirm Transfers remains accessible for each companion.
- View thumbnails from Drive, Samba, and VPS, terminate and reopen the app, then
  revisit the same files. Confirm cached thumbnails avoid thumbnail requests.
  Modify a file and refresh; verify its new version gets a fresh thumbnail.
  Check icon fallbacks, disk-write failure, corrupt entries, missing-version
  expiry, and connection cache removal. Thumbnail eviction must preserve uploads.
  SourcesTests and ThumbnailCacheTests cover aggregation, identity, persistence,
  invalidation, request sharing, isolation, expiry, corruption, and disk limits.
- Play a video from each source and use the native full screen control. Enter
  and exit while playing and paused; confirm position and pause state remain.
  Seek, rotate on iPhone/iPad, and use iPad multitasking. Close with Done and
  confirm streaming stops. Check playback errors in both presentation modes.
  These player transitions and responsive navigation require Apple/device checks.
- These additions have local static validation only until the next explicitly
  requested Apple build/test run; no new simulator or device result is implied.

## Existing browser and transfer checks

- Background-lock regression: refresh a large directory, immediately lock the
  iPad or switch apps, and concurrently browse the same connection in Apple Files.
  Repeat while refreshing source roots and after leaving the app backgrounded
  for several minutes. Confirm both processes remain responsive, catalog changes
  persist, and no RUNNINGBOARD `0xdead10cc` termination occurs. CatalogTests covers
  concurrent store updates and lock release/rollback after a thrown transaction;
  suspension behavior requires device acceptance.

Use a disposable Quantum v2 instance or test account and synthetic files. Do not test mutations against production files. Run XCTest using the shared `myFiles` scheme on a GitHub macOS runner when a build is authorized.

- Connect to two servers, including one mounted under a URL base path. Restart the app and confirm names and credentials persist. Replace an expired token; remove a connection and confirm its Files location disappears.
- Validate rejected HTTP URLs, invalid tokens, disabled API access, insufficient permissions, and v1-style responses. Ensure UI errors contain no credentials or server response bodies.
- Browse nested folders in both layouts on iPhone and iPad, rotate, use split view, dark mode, VoiceOver, and large Dynamic Type. Test empty folders and missing thumbnails.
- Rename and move a folder with children. Confirm existing Files identities remain usable. Delete selected items after confirmation. Verify name collisions never silently overwrite. Include Unicode, spaces, plus signs, ampersands, and percent signs in filenames.
- Copy files and folders from one source to another in the same Quantum connection (for example VPS to Samba). Include identical source/destination paths, a folder copied to the other source’s root, multiple selections, denied destination create permission, and a destination name collision. Confirm originals remain and file contents are not transferred through the phone. Same-source copies into the copied folder or its descendants must remain blocked.
- Upload from Files and Photos, including a large video. Observe progress, cancel, retry after network loss, and confirm collisions retain the local retry copy. No automatic retry of uncertain destructive operations.
- In Files and another app's document picker, browse, open, create, save edits, rename, move, and delete. Verify read-only accounts expose appropriate capabilities. Test connection loss during save, token revocation, and a remote edit before writeback; pending local changes must survive failures.
- Change and delete items using the Quantum web UI, then refresh the app and Files. Confirm deleted items disappear through change enumeration, including deletions initiated by the app.
- Stream a large supported video, seek near its end, interrupt the connection, and reopen after view-token expiry. Test an unsupported codec and a document without a native preview.
- Retest videos that previously failed immediately with CoreMedia -12939. Include MP4s larger than 4 MiB with metadata at the end. Confirm playback starts, forward/backward seeking works, and closing the player cancels outstanding stream requests. The loader must use bounded authenticated ranges on the stream route, without using the download route or creating local playback files.
- Verify motion and an advancing playback clock, including on videos that previously played before the range-loader change. A poster frame or successful metadata load is insufficient. Test pause/resume and playback after seeking. Automated coverage uses an eight-second animated H.264/AAC fixture served through delayed, capped range responses.
- Inspect logs, Git changes, and packaged resources for secrets and private user data before sharing. Verify signed-device behavior for both extension entitlements before a TestFlight upload.

The app and Files extension passed Apple SDK compilation, all 19 simulator unit tests, and device Release archiving on [GitHub macOS/Xcode 26.3](https://github.com/agawlbb/fileBeans/actions/runs/43734190564), source `e335203`. The downloaded archive's checksum and packaged identifiers, version, iOS 18 minimum, iPhone/iPad support, and embedded Files extension were verified. Signed-device checks, Files lifecycle behavior, live-server interoperability, and playback codecs still require the acceptance checks above.

## Companion acceptance

- Configure a companion and verify multi-selection copies submit promptly, keep
  progressing after app termination, and remain visible/cancellable in Transfers.
  Test Samba-to-VPS and a large Drive-to-VPS copy; monitor server traffic to
  confirm the phone receives metadata/progress only.
- Interrupt the service and Samba connection mid-file, reconnect, and compare
  final hashes. Test missing mounts, disk-full, destination collisions, changed
  sources, cancellation, and retries. Confirm unpublished files stay in staging.
- Delete files and folders through fileBeans and its Apple Files location. Check
  each source's Trash icon, retained original name, ten-day expiry, restore with
  missing parents, and restore collisions. Confirm permanent deletion/emptying
  requires confirmation, and an unavailable companion never causes hard deletion.
- Exercise expiry with synthetic entries on test storage. Verify unknown files
  placed in Trash through Quantum survive cleanup. Verify crash reconciliation
  around trash moves, restore, and purge.
- Connect Drive using the existing OAuth client with a separate consent grant;
  browse My Drive, Shared with me, shared drives, and multiple pages. Test an
  unsupported Google-native document/shortcut and duplicate folder names. Revoke
  or replace authorization mid-copy and verify old jobs do not switch accounts.
- Inspect Apple Files Browse, search, working set, and document pickers: there
  must be no Google Drive source or item. A completed copy into VPS/Samba should
  appear normally there. Trash roots and descendants must advertise no write,
  rename, reparent, or delete capability in every enumerator path.
- Recheck an existing connection without a companion for unchanged operations.
  Source syntax validation is local; CompanionTests and iOS acceptance still
  require the normal authorized macOS build/test workflow.

## Full-access Drive acceptance

- Upgrade an existing read-only grant and confirm old same-account jobs continue.
  Switch accounts and confirm old jobs fail clearly. Verify MakeLORA's grant is unchanged.
- Exercise folder creation, rename, within-Drive moves, multi-selection copying in
  both server directions and within Drive, permission-denied items, and collisions.
  Copy Google documents within Drive; open them in Google's browser. Server export
  of Google documents/shortcuts must fail explicitly, including inside folders.
- Upload a large Files/Photos video. Interrupt both phone-to-companion and
  companion-to-Google phases, restart, retry and compare final hashes. Cancel with
  a disconnected server; cancellation must stay pending until acknowledged.
- Verify ordinary Drive thumbnail failures keep icons; folders always use folder
  icons. Play and seek supported Drive media without a local playback download.
- Check root-only bottom Trash shortcuts for VPS, Samba and Drive, with no combined
  trash source. Quantum and Apple Files should retain their existing presentation.
  Trash a synthetic file using drive.google.com and restore it in fileBeans. Check
  shared-drive trash scope, permission restrictions, and permanent confirmations.

The final Drive app source passed all **46** simulator tests and unsigned device
archiving on [validation run 43877396959](https://github.com/agawlbb/fileBeans/actions/runs/43877396959).
Source `a95aa5a` has identical iOS inputs to release tag `testflight/0.1.0-9`
(`7772ca4`); the intervening change is server-only account recovery and docs.
The companion's Go race/MariaDB suite and synthetic live local-storage smoke
checks passed. Actual Google writes await fresh consent and device acceptance.
