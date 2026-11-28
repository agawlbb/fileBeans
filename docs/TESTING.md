## Pi imported thumbnail timestamp compatibility — November 28, 2026

- Read-only live checks found 38 cache entries beside 34 top-level archive media
  files. One media key matched directly; all other 33 matched after truncating
  the media timestamp to Samba's 100-nanosecond precision. Five sampled VPS
  counterparts already served cached thumbnails, while those Pi requests
  returned 404. No media, credentials or caches were changed by diagnosis.
- Pi cache-only lookup now tries the exact revision first, then the former Samba
  revision. Existing linked-video v1 fallback also accepts the Samba revision.
  Non-Pi lookup, public mutation revisions, protection checks, cache size limits
  and Pi generation suppression remain unchanged. No renderer packages added.
- Regression coverage exercises imported JPEG/video caches, exact-key precedence,
  cache-only misses after actual timestamp changes, absence of cache writes,
  Pi-only compatibility, legacy video versions and locked protected-file denial.
  Targeted Pi/Samba, existing cache-only and locked-policy Go regressions pass,
  as does `go vet ./internal/service`. The disposable test container and exact
  `.build/thumbnail-check` directory/backing filesystem were removed. The owner
  subsequently authorized server deployment and app release. Runtime
  source 03ab502 is deployed as HA app 0.1.4. After owner unlock, all 34
  top-level thumbnails and nine nested samples returned HTTP 200/image/jpeg.

## Source-screen and delete corrections — November 28, 2026

- The Pi ordinary-file delete message was traced to the app's cached archive
  check: live resources for `/vps_exchange` and the reported file had no archive
  membership; only `/pics` was marked as the archive. The app could reject deletion
  before contacting the Pi based on an earlier cached flag. Repository deletion
  now delegates archive authorization to the server Trash endpoint, which checks
  the live boundary before mutation. Other source/permission/system-path guards
  remain. Added mocked-server tests for stale-cache acceptance and genuine server
  archive rejection, plus message-format coverage. SDK validation 46097196522
  passed 171 unit/four UI tests; static checks pass.
- Archive messages now display the logical source and a real path when known,
  instead of internal `peer:` coordinates or `.`. Filesystem archive-library
  metadata uses the same public owner/source representation as archive links.
  These formatting/metadata changes passed the full Go service suite and vet
  and are deployed in the server-corrections release.
- Source `cf02413` removes repeated source captions, replaces the legacy default
  File Browser Quantum name with VPS for the existing VPS endpoint, and opens a
  single-root server directly without an intermediate folder. Multiple roots stay
  selectable; connection/credential IDs and custom names are preserved. Validation
  [46092196122](https://github.com/agawlbb/fileBeans/actions/runs/46092196122)
  passed 168 unit tests and four UI tests. The corrections are included in signed
  build 57; the exact IPA was uploaded in run 46097745266.
- Source `45e024f` upgrades a readable legacy archive-policy cache directly instead
  of requiring the separately protected original link to be readable. It verifies
  the original-link revision, adds the existing redacted boundary format, preserves
  policy encryption/AAD and the original-link bytes, and updates only an unchanged
  policy row. The SQLite regression actually moves an ordinary file to Trash with
  the Archive locked, checks ciphertext preservation, then checks ordinary guards
  with the Library also locked and verifies the Archive remains read-only. Related
  locked-boundary, mirror, handoff and preview regressions plus Go vet pass.
- Live inspection found VPS unlocked, Pi locked, and the legacy policy missing
  its boundary. After the owner unlocked Pi separately, the running server's
  existing fallback refreshed that cache; its boundary is now present. The source
  correction prevents that initial upgrade from requiring the Archive key. Pi
  visibility needed no code change and is resolved by the owner's unlock.
- The owner explicitly authorized server deployment and app release. VPS runtime
  03ab502 and HA app 0.1.4 are deployed and verified. Signed app 57 passed the
  larger-runner SDK suite and was uploaded without rebuilding; Apple build 57
  is VALID / IN_BETA_TESTING. Previously started build 56 was never uploaded,
  and its signed artifact was removed.
- Disposable Go regression tooling, dependencies, caches, outputs, container
  `filebeans-delete-check`, and `.build/delete-check` / `.build/delete-check.ext4`
  were removed after verification. No task artifact is retained for these fixes.

## Thumbnail and streaming performance — November 27, 2026

- Thumbnail regressions cover synchronous decoded-image reuse, shared fetch/decode,
  disk fallback after memory warnings, revision/expiry invalidation and connection
  removal during an outstanding load. Scroll away and back in filesystem and Drive
  grids/lists: unchanged cached thumbnails should appear immediately. Lock protected
  folders and verify cached images cannot appear until unlocked.
- Video regressions cover progressive delivery before HTTP completion, strict range
  headers/truncation checks, overlapping cached/in-flight reads, cancellation,
  least-recently-used eviction, and zero extra requests when revisiting a cached range.
  The controlled transport fixture holds completion for 200 ms after first delivery
  and prints first-byte time against the whole-response baseline. H.264/HEVC tests
  print seek-to-moving-frame latency and require under four seconds on delayed,
  capped responses; physical-device latency still requires acceptance below.
- On iPhone/iPad, compare repeated forward/backward seeks on VPS, Samba and Drive,
  including a larger high-bitrate video. Confirm quick motion after scrubbing,
  automatic recovery after throttling, intentional pause preserved during recovery,
  and no new stream/player when toggling full screen. Paging away stops requests;
  returning to a retained neighbor reuses fetched ranges. Closing/eviction releases
  video ranges. No persistent movie download is created.
- Source changes require a requested Apple app build/release before device acceptance.
  No server API or memory-limit changes belong to this client correction.
- Final source `a5ef6fb` passed [simulator validation 46002293143](https://github.com/agawlbb/fileBeans/actions/runs/46002293143):
  156 unit and four viewer UI tests, all first attempts. Repeated H.264/HEVC seeks
  reached decoded frames with 0.5 seconds of playback progress in 0.68–0.85 seconds.
  Synthetic first-byte delivery preceded held response completion by over 200 ms.
  This validates client behavior; VPS/Samba/Drive device acceptance remains pending.

## Folder viewer swipes

- TestFlight 38 regression correction: the video UI fixture streams the animated
  playback probe through bounded range responses. Its UI test checks that only
  one full-screen control exists and that the same attached item keeps decoding
  through three enter/exit cycles. Playback now belongs to the cached movie;
  paging away and cache disposal stop it. The video has no nested navigation
  stack or native full-screen presentation. Run simulator validation with
  `tests_only=true` to exercise the correction without producing a device archive.
  Correction `4d91955` passed [simulator validation
  45713689672](https://github.com/agawlbb/fileBeans/actions/runs/45713689672):
  136 unit tests and four UI scenarios. The new streamed-video transition test
  and cache-lifecycle test passed on their first attempts. The existing photo
  expansion test timed out initially, then passed on its configured retry.
  Corrected TestFlight 0.1.0 (39) is VALID / IN_BETA_TESTING;
  iPhone/iPad device acceptance remains pending.

- Video playback regression: repeatedly seek forward/backward while playing,
  enter/exit full screen after each seek, and repeat while paused. Confirm moving
  frames, an advancing clock and preserved position, with no white strip at the
  bottom in portrait/landscape on iPhone and iPad. Throttle the connection until
  playback buffers, restore it, and confirm playback resumes without touching
  pause/play. Pause during buffering and confirm it stays paused after recovery.
  Dismiss or page away while buffering and confirm streaming stops. Repeat from
  VPS, Samba and Drive. The repeated-seek automated tests passed in
  [Apple validation 45707728126](https://github.com/agawlbb/fileBeans/actions/runs/45707728126)
  with 135 unit and 3 UI tests. TestFlight 0.1.0 (38) is VALID / IN_BETA_TESTING;
  the device checks above remain manual acceptance work.

- Full screen: open a photo from VPS, Samba and Drive, tap **Full Screen**, and
  confirm the image uses the screen edges and system bars hide. Swipe to the
  next and previous photo/video; full screen must remain selected with no
  Full Screen/Done buttons visible. A single tap exits full screen and restores
  the controls; swiping in either direction must not exit. Confirm the selected
  file is retained after tapping. For a video, toggle while
  playing and paused: playback position, pause state and the existing stream
  must survive. Check rotation, image zoom/pan and Done on iPhone and iPad.
  Leave photos and paused/playing videos visible beyond the Auto-Lock interval
  in both modes; the screen must stay awake. Close the viewer or background the
  app and confirm normal dimming returns. The UI regression covers photo
  expansion, paging, mode retention, selection and dismissal; Apple execution
  and physical-device acceptance remain pending for this change.

- Adjacent prefetch: open a middle image in a VPS, Samba or Drive folder and
  verify only the selected file and its immediate image/video neighbors load.
  Swipe both ways and confirm cached images open without another download.
  Neighboring videos fetch at most 256 KiB each without playing; opening them
  reuses those bytes and continues streaming with bounded range requests.
  Check first/last boundaries and a document between images. Rapidly swipe,
  dismiss during downloads, retry after a failed neighbor request, and trigger
  a memory warning; no late results or temporary previews should survive
  eviction/dismissal. Lock protection while viewing and confirm dismissal and
  cache cleanup. Repeat with slow networking and video full screen controls.
- FolderViewerTests cover neighbor reuse, foreground-first loading, document
  exclusion, eviction, memory trimming and late cancelled-download cleanup.
  MediaStreamTests cover the bounded head request and reuse during playback.
  These checks passed with 123 unit tests and 2 UI gesture tests in
  [build-32 simulator validation](https://github.com/agawlbb/fileBeans/actions/runs/44960712154).
  The duplicate unsigned archive step was cancelled after the signed Release
  archive/export succeeded. Signed **0.1.0 (32)** is VALID / IN_BETA_TESTING;
  mixed-media and slow-network physical-device checks remain manual.

Source `02cb723` passed 119 unit tests and 2 real UI gesture tests in
[Apple validation 44910102053](https://github.com/agawlbb/fileBeans/actions/runs/44910102053).
The UI tests cover photo swipes in both directions, first/last boundaries, full
screen continuity, and zoom/pan followed by resuming paging. Unit coverage checks
remote-photo eligibility and preservation of the downloaded image during display.


- In VPS, Samba, and Drive folders containing photos, videos, PDFs, unsupported files and subfolders, open a supported file and swipe left for next and right for previous. Confirm browser sort/search order, skipping folders, unsupported files, pending transfers and files without download permission. Confirm paging stops at either end.
- Move between videos, photos and multi-page PDFs while full screen; confirm the presentation stays full screen. Check PDF scrolling, image zoom, video seeking, rotation and iPad layouts. Confirm only the selected video plays, leaving a video stops its stream, and Done closes the viewer.
- Swipe during a slow download, dismiss during loading, and retry a failed load. Confirm no late presentation, stale playback or leftover downloaded previews. Drive paging covers the folder entries currently loaded in the browser.

# Acceptance checks

## Text files

- In a writable VPS or Samba folder, choose **Add → New Text File**, enter a name and text, and save. Confirm explicit extensions (`notes.md`, `config.yaml`) and dotfiles (`.env`) are preserved, `.txt` is appended to a bare name, empty files save, and duplicates fail without replacing existing files.
- Use **Edit Text** on Markdown, JSON, source/config files, files with unfamiliar extensions and extensionless files. UTF-8 and BOM-marked UTF-16/UTF-32 are supported; verify encoding/BOM and line endings survive saving. Cancel with changes and verify Discard leaves the file unchanged. Check failed saves retain the draft and read-only files have no edit action.
- Change the same file from another client after opening the editor; saving must report a conflict and preserve the newer remote file. Verify locked protected folders reject saves. Binary/invalid text and files larger than 2 MiB must fail without replacing contents; verify the download limit also applies when Content-Length is absent. EditableTextTests and FilesystemClientTests cover content validation, encoding preservation, filenames and bounded transfers.
- Text creation/editing currently covers filesystem sources; Drive retains its existing preview/upload controls. Apple compilation and device acceptance are still required.

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
  then return to the source list. Reopen the same source repeatedly, switch
  between Samba, VPS, and Drive, and back out while a listing is still loading.
  Confirm each opening loads without cancellation alerts and Drive retains its
  authorization. Repeat on iPad with the source sidebar visible. If the companion
  cannot be reached, Drive should offer Retry rather than ask for sign-in before
  its connection status is known.
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
- Source `f8957ca` passed all **62** simulator tests and the unsigned iPhone/iPad
  Release archive on [validation run 43929950557](https://github.com/agawlbb/fileBeans/actions/runs/43929950557).
  The downloaded result bundle confirms the successful test total; its checksum
  and the unsigned archive's checksum match GitHub's artifact digests. Signed
  build **0.1.0 (11)** is VALID / IN_BETA_TESTING after
  [release run 43932404308](https://github.com/agawlbb/fileBeans/actions/runs/43932404308).
  Responsive navigation and full screen transitions still need device acceptance.

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

## Navigation reopening release validation

Source `de85cab` passed the simulator suite and unsigned iPhone/iPad Release
archive on [validation run 43935825811](https://github.com/agawlbb/fileBeans/actions/runs/43935825811).
Both downloaded artifacts matched GitHub's SHA-256 digests; app and extension
identities, iOS 18 minimum, and iPhone/iPad support were verified. Signed build
**0.1.0 (12)** is VALID / IN_BETA_TESTING after
[release run 43939202252](https://github.com/agawlbb/fileBeans/actions/runs/43939202252).
The source-list back/reopen regression still requires the physical-device checks
above; the model suite does not exercise SwiftUI compact navigation transitions.

## Compact source navigation follow-up

The compact layout uses a stable `NavigationStack` with value-based source links
instead of programmatic split-column changes and replacement of the detail stack.
The regular layout retains its source sidebar. `SourceNavigationTests` hosts the
same navigation container with synthetic asynchronous listings, checks that
popping cancels the old destination task, and requires a new listing to complete
when reopening either the same source or different sources.

These new lifecycle tests require the next authorized Apple simulator validation;
local syntax validation alone does not establish that they pass. Device acceptance
must include Samba → back → VPS → back → Drive and repeated reopening, rapid back
while loading, nested folders, list/grid, Transfers, and connection edits. On iPad,
check source switching with the sidebar visible and compact multitasking widths.

## Folder breadcrumb navigation

- Browse several folders deep in Samba and VPS. The inline title shows the source
  and every visited folder; tap an ancestor and confirm the stack returns to that
  existing folder with working refresh, back navigation, and file actions.
- Repeat in Google Drive, Shared with me, and shared drives. Include duplicate
  folder names and non-path-like Drive IDs; ancestor links must retain the correct
  Drive folder identity and account.
- Use long folder names, deep nesting, portrait, landscape, and larger text. The
  title falls back to the source and an overflow hierarchy menu, with the current
  name abbreviated where space allows. The source must remain available; every
  ancestor and Sources must remain reachable through the menu.
- On iPad, confirm the sidebar's Current folder hierarchy follows nested browsing,
  native back navigation, source changes, and ancestor taps. Tapping the source
  returns to its root. Check both list and grid layouts and multiple connections.
- `FolderBreadcrumbTests` covers ancestor truncation, invalid indices, and opaque
  Drive IDs with duplicate names. Static source validation passed locally; the
  new tests and layout/lifecycle acceptance require Apple validation and devices.

## TestFlight 13 validation

Source `f5120ad` passed simulator tests and unsigned iPhone/iPad Release archiving
on [validation run 43946193403](https://github.com/agawlbb/fileBeans/actions/runs/43946193403).
Both `SourceNavigationTests` passed (same-source reopening and different-source
switching), as did all three `FolderBreadcrumbTests`. Both retained artifact
checksums matched GitHub's SHA-256 digests, and packaged identities and supported
devices were verified. Build **0.1.0 (13)** is VALID / IN_BETA_TESTING following
[release run 43949614169](https://github.com/agawlbb/fileBeans/actions/runs/43949614169).
These synthetic navigation tests do not replace live-source device checks or
narrow-width breadcrumb/iPad sidebar layout acceptance above.

## Folder heading placement correction

The hierarchy replaces the original large folder-title heading beneath the
navigation controls. The native text title is suppressed so there is one heading
row, rather than a folder title plus an additional breadcrumb row. It uses
large-title typography, full available width, and 44-point-minimum tap targets.
When the full path does not fit, the source and full-hierarchy menu remain
available; the current folder is abbreviated as space permits. The iPad sidebar
continues to show the full active hierarchy.

Check there is only one large heading row on iPhone and iPad, including at source
roots, with deep paths, long names, larger text, and narrow landscape/split widths.
Verify ancestor taps, overflow access, native back navigation, and scrolling.
Static checks do not replace Apple layout/device acceptance; this correction has
been validated and published as build 14; layout acceptance remains manual.

## Heading correction build validation

Source `b7ac1b9` passed simulator tests and unsigned iPhone/iPad archiving on
[validation run 43952665641](https://github.com/agawlbb/fileBeans/actions/runs/43952665641).
The signed **0.1.0 (14)** IPA was built and retained by
[build run 43956876063](https://github.com/agawlbb/fileBeans/actions/runs/43956876063).
Downloaded validation and signed artifacts matched GitHub SHA-256 digests;
packaged app/extension versions and identities were verified. Upload was skipped.
Heading layout and physical-device acceptance remain manual.

## Build 14 TestFlight publication

Following explicit publication approval, source `b7ac1b9` was tagged
`testflight/0.1.0-14` and published through
[run 43957926938](https://github.com/agawlbb/fileBeans/actions/runs/43957926938).
Preflight, signed archive/export, upload, and credential cleanup passed.
Apple build `5e839cb5-480c-4c26-b50f-2916caadd20a` reports VALID and
IN_BETA_TESTING; the existing internal group retains access to all builds.
The heading layout acceptance checks above still require physical-device review.

## Compact hierarchy with Sources root

The heading uses subheadline-size text to fit more ancestors in the same row.
Every width fallback keeps a direct Sources button; ancestor overflow remains
available. The iPad browsing sidebar shows only Sources → selected source → open
folders, with the same smaller type. Tapping Sources clears selection and returns
to the source picker; source choices and Transfers appear there rather than being
repeated below the active hierarchy. Settings remains available while browsing.

Verify Sources from both heading and sidebar, opening another source afterwards,
long/deep paths and Dynamic Type, list/grid, and all three heading width fallbacks.
Static checks do not substitute for Apple layout/device acceptance; this follow-up
is validated and published as build 15; physical layout acceptance remains manual.

## Build 15 release validation

Source `952950a` passed simulator tests and unsigned iPhone/iPad archiving on
[validation run 43960236333](https://github.com/agawlbb/fileBeans/actions/runs/43960236333).
Downloaded validation artifacts matched GitHub SHA-256 digests; packaged app and
extension identities and supported devices were verified. Signed build
**0.1.0 (15)** was published by
[run 43964841908](https://github.com/agawlbb/fileBeans/actions/runs/43964841908).
Apple reports VALID / IN_BETA_TESTING, with the existing internal group retaining
access. Smaller path text, direct Sources navigation, and the hierarchy-only iPad
sidebar still require physical-device layout acceptance.

## Transfer cancellation, details, and small-file performance

Source `84ecad1` passed all **68** simulator tests and unsigned iPhone/iPad Release
archiving on [validation run 43976688057](https://github.com/agawlbb/fileBeans/actions/runs/43976688057).
The new queued-job filename/destination decoding regression passed. Both downloaded
artifacts matched GitHub SHA-256 digests; app and extension identifiers, version
0.1.0, iOS 18 minimum, and iPhone/iPad support were verified. The unsigned validation
archive uses its default build number 1; the separate signed release overrides both
targets to build 16.

Transfer controls show cancellation immediately, retain server-acknowledged
cancellation status, display submitted source/destination and Drive names for new
submissions, and filter completed jobs from the list. On device, check cancelling
queued and active copies, app relaunch while cancelling, long filenames, errors,
and completed-job disappearance. These SwiftUI/device checks remain manual.

The companion Go race/MariaDB suite, Go vet, and source/config checks passed.
Tests cover four overlapping copies and Drive listings, deterministic discovery,
pagination/duplicate/incomplete-listing rejection, large-file serialization,
all-worker cancellation, corrupted-prefix and uncheckpointed-write recovery,
source changes, bounded directory descriptors, permissions, symlinks, aliases,
and replaced/unexpected staging objects. The 128-file synthetic latency fixture
was about 3.8x faster with four workers than one using the same optimized pipeline.

The companion-only VPS deployment preserved the running normalizer's container ID
and start time. An isolated 32-file VPS-to-Samba probe improved from 30.757 seconds
to 8.729 seconds, with matching hashes and probe-file cleanup. This does not
measure Drive download bandwidth or attribute a separate speedup to each change.
Authenticated HTTPS, access denial, copy, trash/restore/conflict/purge, and Drive
browsing smoke checks also passed. See the deployment record in the
[companion README](../companion/README.md).

Signed build **0.1.0 (16)** was published through
[run 43978748344](https://github.com/agawlbb/fileBeans/actions/runs/43978748344),
including successful temporary credential cleanup. Apple build
`e0be188e-e5cc-4be6-af00-3b63598cbca3` reports **VALID** / **IN_BETA_TESTING**.
The companion-only deployment and final service inspection confirmed the video
normalizer was not restarted. Device acceptance above remains manual.

## Rclone copy-engine replacement — November 17, 2026

VPS/Samba destination copies now run the pinned rclone executable. The custom
per-file worker, directory cache, prefix hashes and batched manifest patcher were
removed. Tests exercise the real rclone binary on 512 small files, empty folders,
existing destination conflicts, staged hardlinks, corrupted/incomplete files,
ordinary copy preservation of existing staging, and source updates between
attempts. Subprocess fixtures cover Drive ID routing, private credential handling,
actionable redacted errors, cancellation, and account changes without real Google
requests. The full Go race/MariaDB suite, Go vet, and source checks passed. Drive
upload and media-normalization regression suites also passed.

The iOS request/response contract is unchanged, so existing TestFlight build 16 can
use this backend. Local fixture timing is not a Drive/Samba performance claim;
production measurements and transfer completion must be recorded separately.

## Shared-user deletion regression — November 18, 2026

Delete verification must include the Trash preparation step with different
Quantum and companion UIDs sharing the configured GID. Existing SQL lifecycle
and transfer tests did not exercise this complete permission boundary.

The regression reproduced `chmodat Trash: operation not permitted` for ordinary
and protected roots when Quantum owned an already group-writable Trash folder.
The fix reuses shared-directory permission validation rather than requiring
ownership to chmod every existing Trash directory.

Focused MariaDB/API tests cover files and nested/empty folders on both configured
sources: delete intent/retention, restore with matching contents, repeated delete,
confirmation-required permanent deletion, and the persisted purge checkpoint.
The isolated local FUSE and kernel CIFS fixtures create sources and Trash roots
as Quantum, then run the production retention helper as the separate companion
UID. They verify delete, restore and permanent deletion for files and folders,
including retained contents and empty directories.

After deployment, authenticated HTTPS checks exercised all six combinations of
file/folder and VPS/ordinary Samba/protected Samba. Fixtures were created as
Quantum UID 10001/GID 1000 and operated on by the actual companion API. All six
passed delete, retained-byte verification, restore, repeated delete and purge.
Only disposable fixtures were removed. The existing protected folder remained
unlocked. These are server-path checks; they do not claim physical-device UI tests.

## Six file-management changes — November 18, 2026

The focused server tests cover private encrypted Trash, locked-entry omission,
legacy-location migration and recovery at intent/rename boundaries, preservation
of unrecognized contents, cleanup of tracked empty failed-delete directories,
and restoration after the original parent has been removed. Full Go race/MariaDB
checks pass with the previously established staged-hardlink baseline excluded.

The real local FUSE and kernel CIFS fixtures exercise private Trash retention,
restore and permanent deletion under separate consumer UIDs, plus hidden locked
roots through Quantum's actual resource handler. The patched Quantum race suites
and runtime image checks also pass.

Live authenticated HTTPS verified file and folder delete/restore/re-delete/purge
on VPS, ordinary Samba and protected Samba. All six cases matched retained and
restored contents. A three-file copy into the protected folder and three-file
move out each ran as a single job; hashes and original retention/removal matched
the requested operation. Only disposable fixtures were purged. The existing
retained protected item migrated with its metadata/content-prefix fingerprint
unchanged, and the old public per-folder Trash directory was removed.

Apple validation 44330888062 passed on source `708525a`, including all eight
FileOperationTests and the batch-submission transport regression, followed by
the unsigned iPhone/iPad Release archive. These cases cover immediate whole-
selection delete state, per-item completion/failure, destination placeholders
before acknowledgement, duplicate suppression, terminal-job cleanup/error,
server-job reconstruction, renamed targets, authenticated scope mapping, queued
batch-move source state, locked-folder identity and legacy Trash cache removal.
The tests establish server and application-state behavior; physical-device
interaction acceptance remains separate.

## Settings, sidebar navigation, and protected Files exclusion

The source changes include regression tests for protected File Provider access
in locked, unlocked, unavailable, and scoped-source cases, plus an iPad test
that taps the current sidebar path and verifies the detail is recreated.
[Apple validation 44441308287](https://github.com/agawlbb/fileBeans/actions/runs/44441308287)
passed all 107 simulator tests with zero failures and the unsigned iPhone/iPad
Release archive on source `8f346f9`. VM static parsing and source/configuration
isolation checks also passed. Physical-device acceptance below remains separate.

On iPad, open Transfers, Trash, and Media Normalization from a folder, then tap
that folder or an ancestor in the sidebar. The active detail must return to the
selected folder, including when the folder path itself did not change. Also
return to Sources and choose the same or another source.

Verify Settings starts with the connection's Lock/Unlock control, contains
encryption-key tools and Google Drive connection tools, and contains no Transfers
entry. Transfers stays in folder top bars and appears in the Sources top bar;
multiple connections offer a connection chooser. Lock protection and verify
Add Protected Folder disappears; unlock and verify it returns for eligible
ordinary folders.

From ordinary folders and writable Drive folders, use Upload Photos or Videos
to select photos, videos, and multiple items. Verify the picker opens, uploads
complete, cancellation leaves no upload, and failed media loading shows an error.

With protection both locked and unlocked, verify Apple Files Browse, search,
working set, and document pickers contain neither protected folders nor their
descendants. Check an upgraded installation with previously enumerated protected
items: change enumeration must withdraw those items and cached identifiers must
not permit metadata, content, thumbnails, edits, or protected move destinations.
Unlocked protected content must remain usable inside fileBeans.

## Sidebar routing, upload transfers, and cross-Drive moves — build 25

[Validation 44450075330](https://github.com/agawlbb/fileBeans/actions/runs/44450075330)
passed all 109 simulator tests and the iPhone/iPad Release archive. The new
navigation regression opens local Trash, Drive Trash, Transfers, Media
Normalization, and protection, then returns to the source through the sidebar.
The navigation audit leaves direct destination links only inside modal folder
pickers; main-screen destinations use the tracked source navigation path.
Upload tests cover connection isolation, progress, identity across the server
job handoff, and removal after completion. Destination tests cover cross-Drive
moves and the retained legacy local-source restriction.

VM Go race tests and vet passed, including isolated database-backed move tests:
local-to-Drive completion, lost upload-response recovery, source changes,
cancellation, Drive-to-local published-copy cleanup, lost trash-response
recovery, and changed destination/source retention. The cloud fixture resets
upload response objects to avoid aliasing previous uploads' metadata.

Production checks verified authenticated public source discovery, the new
`crossDriveMoves` capability, Drive metadata reads, zero API restarts, and no
OOM termination. The normalizer, broker, and API configuration were unchanged.
A live round-trip write test was not run: automatic approval review rejected
the original remote move/purge test. A revised disposable-file test with cleanup
to Trash only awaits explicit approval; no test fixtures were created remotely.

Physical-device acceptance: verify sidebar returns from both Trash screens and
all special screens, including repeated taps on the current folder and Sources.
Check Transfers stays above main content on iPad Sources, local/Drive folders,
Trash, and Normalization. Start local and Drive uploads, open Transfers, and
verify queued/uploading progress and a single row across the server-job handoff.
Check a failed or cancelled copy retains its original; completed moves remove
their original only after destination publication.
# Local move rename and hashing checks — November 19, 2026

The Go race suite passed with pinned rclone 1.76.0 and the isolated VM test
database. Follow-up focused race tests and `go vet ./...` passed after recovery
and mixed-batch refinements. The development image's older system rclone caused
unrelated hard-link/critical-error test failures; selecting the shipped pinned
binary resolved those failures without changing copy behavior.

Coverage includes sparse-file rename without content scans, interrupted rename
recovery, replacement source preservation, destination conflicts, sync-error
recovery, mixed rename/copy batches with normalized paths, actual cross-device
copy fallback, source/target edits, cancellation, and existing move-journal
restart phases. Isolated real FUSE/CIFS checks passed for protection boundaries,
within-vault rename eligibility, and rejection of vault-root moves. Inactivity
and protection-gate tests passed.

The deployed server passed direct/public health checks and disposable Samba
moves within ordinary storage and into/out of unlocked protected storage.
Ordinary rename retained its inode. Protected copies retained their bytes and
removed their sources; the return job exposed `preparing` before copying.
Fixtures were cleaned. The existing cancelled archive job was preserved.
See the corresponding deployment record in `RELEASING.md`.

## Drive destination version changes — November 19, 2026

Read-only inspection of the reported Samba-to-Drive failures found publication
complete but source removal not committed. One retained Samba source still
matched its recorded size, timestamp and inode; its Drive destination retained
its name and size while the Drive version advanced from 2 to 5. A later copy
completed. These observations do not establish a live content-checksum match.

Drive destination snapshots now retain the available SHA-256 or MD5 checksum.
Before move cleanup, equal checksums permit destination version changes;
directory version changes are also allowed. Identity, path, type, size,
content and parent checks remain. Source comparisons stay strict. Snapshots
without a checksum still require the same file version.

The new metadata-change regression reproduces the conflict against the previous
implementation and passes with the fix. It rejects changed checksum, size, name,
parent and unverifiable version changes. The isolated MariaDB regression verifies
Samba source removal after a matching-checksum version change. The full companion
Go race suite with the pinned rclone and VM test database, and Go vet, passed.
No production files or jobs were changed. Companion deployment and live acceptance
remain pending; no app build is required for this server fix.

### Transfer failure notice history (November 20, 2026)

Read-only inspection of all 38 retained production copy/move jobs found 34
completed, two cancelled, and two failed. Both failures were older Samba-to-Drive
moves; the latest VPS-to-Samba selections were completed with every selection
published. No production files or jobs were changed.

The app now separates active transfers and new failure notices from history.
Previously retained failures no longer appear as current transfers. Newly
observed failures remain visible for 30 seconds or until dismissed; repeated
polling does not extend that deadline. History includes successful, failed and
cancelled transfers. A retry or later completion clears the job's failure alert.
Regression cases cover initial history, expiry, dismissal, recovery and error
isolation. Local Swift syntax/project validation passed; execution of these
XCTest cases remains pending on macOS. Delivery requires an app build, with no
companion deployment needed for this display correction.

## Download selected files — November 24, 2026

Local and Drive file menus and selection toolbars now offer Download. Only
non-pending downloadable files qualify; folders and native Google documents
disable the selection action. Downloads prepare sequentially, preserve names in
isolated directories, and open the Files export picker. Staging is removed after
export, cancellation, preparation failure, or release of the download owner.

The three changed Swift files passed tree-sitter syntax parsing and diff checks
in development. Apple compilation and device acceptance remain pending an
explicitly requested app build. Verify single and multiple files on both sources,
duplicate filenames, Files picker cancellation, download failure, denied download
permissions, and compact iPhone/iPad toolbar layout. No server deployment needed.

## Public file hosting — November 24, 2026

Focused Go race tests and service vet passed in an isolated development container
with synthetic storage and the VM test database. Coverage includes authenticated
management, anonymous attachment downloads, HEAD/ranges, Unicode/reserved filename
escaping, readable collision suffixes, restart persistence, expiry/renewal,
repeatable revocation and reserved slugs, original replacement, forged protected
requests, locked/unlocked/error broker snapshots, missing snapshots, internal
paths, symlinks, directories, Drive capabilities/native-document rejection,
content revision changes and Drive account changes. No production files changed.
The disposable 3 GiB test filesystem/container and helper were removed.

Swift syntax parsing and diff checks passed. Added Apple transport regressions
cover canonical scoped paths, token-free filename-ending links, and duration /
revocation requests; execution and app compilation remain pending on macOS.
On iPhone/iPad, verify Host File for one ordinary local/Drive file (including
selection mode), absence for protected files in every lock state, both durations,
copy/share links, Settings listing, changing expiry, and Stop Hosting. Check
compact toolbar and date-picker layout. After an explicitly requested API deploy,
verify the public proxy's filename-ending URL anonymously downloads the expected
bytes and expired/revoked links fail. No deployment or app build was requested.

### TestFlight 35 Apple verification

Source `ce6b8ad` passed all 132 unit tests (including both new hosting transport
regressions) and 3 UI tests, plus the unsigned iPhone/iPad Release archive in
[validation 45454189648](https://github.com/agawlbb/fileBeans/actions/runs/45454189648).
[Publication 45456586281](https://github.com/agawlbb/fileBeans/actions/runs/45456586281)
released internal TestFlight 0.1.0 (35); Apple reports VALID / IN_BETA_TESTING.
This resolves the Apple execution/build prerequisites above for downloads,
full-screen controls and hosting. Physical-device acceptance remains manual.
The hosting API deployment and anonymous live hosting checks remain pending;
no server deployment was part of the app release.

### Video thumbnails — November 26, 2026

Video previews sample at two frames per second within the first thirty seconds,
discard frames with at least 98% near-black pixels, and select a representative
frame from up to twelve remaining samples. Samples are scaled before batching.
Short clips and clips without an eligible sample fall back to their first frame,
including FFmpeg versions that report empty filtered output as an error. The
existing renderer timeout, pinned descriptors and protected scratch paths remain.
Frame metadata and selection use FFmpeg's documented
[blackframe](https://ffmpeg.org/ffmpeg-filters.html#blackframe) and
[thumbnail](https://ffmpeg.org/ffmpeg-filters.html#thumbnail) filters.

Only video cache versions change on the server and in the app. Automatic VPS
backfill refreshes video previews and copies the new cache to matching linked
archive originals without opening either media file for the copy. An existing
imported archive preview remains available until its new copy arrives; archive
requests remain cache-only. Images, documents and source video bytes are preserved.

Focused Go race tests and service vet pass, including synthetic videos with
two-/six-second black openings, a 0.1-second clip, an all-black clip, legacy video
caches, cache-only archive refresh, existing video/PDF renderers under the
protected-process policy, thumbnail backfill and overnight idle regressions.
The changed Swift source passes syntax parsing. Full `validate-source.py` stops
at its existing target-list assertion: `myFilesUITests` is present in project.yml
but absent from that assertion. Apple compilation/runtime checks remain pending.

API deployment and an app build/release are still required; neither was requested
or performed. The resource-limited VM test container and its bounded 2 GiB test
filesystem, renderer bundle, toolchain, fixtures and caches were removed after
verification. No production changes were made.

### Rename selection — November 26, 2026

Filesystem and Google Drive Rename dialogs initialize the text selection to the
filename before its final extension, using SwiftUI's
[TextSelection](https://developer.apple.com/documentation/swiftui/textselection).
Folders, native Drive document titles, extensionless names, leading-dot names
without another extension, and names ending in a dot are selected in full.
Selection state resets on dismissal. New Folder controls are unchanged.

The three changed Swift files pass syntax parsing and the patch passes whitespace
checks. App compilation and device/simulator acceptance await an explicitly
requested app build. Verify that immediate typing replaces `report.final` in
`report.final.pdf`, preserves `.pdf`, works with Unicode names, and selects all
of `.env` and a folder named `Photos.2026`; repeat after cancelling and reopening
in both browsers. No server deployment or temporary artifacts are required.

### Archive launch notice — November 26, 2026

App launch and foreground activation no longer request archive status or present
the Archive Backup Pending alert. Upload recovery and memory-warning handling
remain; server archive processing, original retention and automatic retries are
unchanged. The changed Swift source passes syntax parsing and whitespace checks.
After an explicitly requested app build, verify launch and repeated background /
foreground transitions while archive work is pending produce no popup and uploads
still recover. No server deployment or temporary artifacts are required.

### Server transfer reconnect after backgrounding — November 26, 2026

Copy/move submission, transfer-list refresh and move completion polling reconnect
once after a timeout or interrupted network connection. The app waits until it
is active before reconnecting. Submission retries reuse the exact encoded body
and persisted idempotency key, so an operation completed while the app was closed
returns its existing completed job instead of creating a second copy or move.
Failed reconnects preserve the key for a later retry; permission errors and actual
failed jobs remain errors. Cancelled/background transfer-list refreshes no longer
publish stale UI errors. The File Provider has no app-foreground dependency and
uses the same bounded transport retry immediately.

Five transport regressions cover foreground gating, lost submission responses,
bounded retry with request-key reuse across client recreation, job-list/move-poll
reconnects, and permission rejection. These tests await Apple execution; no Swift
runner is installed in the VM development container. All six changed Swift files,
including the regressions and new app-only foreground helper, pass syntax parsing
and whitespace checks. No app build, deployment or temporary artifacts were made.

After an explicitly requested app build, run the transport tests and start long
copy/move jobs, background the app, let the server complete them, then reopen.
Verify completed status, refreshed destination/source listings, no duplicate job
or stale timeout alert, and continued visibility of genuine network/server errors.

### Archive status in Transfers — November 26, 2026

The transfer status icon opens the existing Transfers screen with an Archive
section. Visible files show queued transcoding/copying, live transcoding/copying,
finishing, retry time/error, or attention; empty work shows Up to date. Regular
transfers and archive work refresh independently every two seconds while this
screen is active. There is no startup popup or fabricated percentage progress.
Older servers show their available summary counts. Locked work shows counts
without names; viewing status never unlocks a folder.

The additive archive status API retains its previous count fields and adds total
unfinished work plus visible item details. The worker activity map stores only
IDs/phases, uses a mutex, and protects a newer worker from an older worker cleanup.
Go race tests cover phase mapping, activity/retry transitions, hidden/stale worker
visibility and concurrent workers; related archive protection/retry and copy
collision/bounds tests and service vet pass. Swift status decoding/reconnect
regressions are added; all five changed Swift files pass syntax parsing. Apple
execution and database-backed HTTP integration remain pending: the VM has no
Swift runner or fileBeans-specific `.test-config` database file.

API build/deployment and an app build/release require explicit requests. Acceptance
should open the transfer icon during both encoding and original backup, verify
the per-file transition and offline retry, then lock protected folders and confirm
names disappear. The bounded VM test filesystem, toolchain, caches and container
were removed after verification. No production changes were made.

### Archive encoder memory failure — November 26, 2026

NReading.mp4's four failed attempts corresponded to kernel segmentation faults in
libx265 at 10:54:29, 10:55:33, 11:00:40 and 11:15:46 UTC. Container OOM counters
were zero. Read-only metadata inspection identified a 720x1280, 30 fps H.264/AAC
clip lasting 9.966667 seconds. Production and development both use FFmpeg
7.1.5-0+deb13u1 and x265 4.1-2.

The cause was the archive worker's encoding configuration: x265's default buffered
frames exceeded the existing 512 MiB process-data allocation cap. A synthetic
720x1280 reproduction under the same 2 GiB address / 512 MiB data caps reported
`malloc of size 2764800 failed`, then `memory allocation failure, aborting encode`
and exited with SIGSEGV. Allocator-only changes still failed. Reducing lookahead
alone succeeded at 720x1280 but still failed at larger supported resolutions.

The initial mitigation in `90e0b0e` used one frame thread, one reference, no B frames or
lookahead, and no CU-tree buffering. Its HEVC 10-bit format, CRF-18 quality target,
medium preset, audio policy and resolution limits are preserved. The tradeoff is
less compression efficiency and potentially larger files. All existing process,
container, validation, disk-reserve and original-retention limits remain intact.
The upstream [x265 options](https://x265.readthedocs.io/en/master/cli.html#cmdoption-bframes)
document buffered-frame memory costs and the low-latency P/I-frame mode.

Race-enabled integration tests passed in a 1 GiB/no-swap, two-CPU VM container:
synthetic H.264/AAC at 720x1280/30 fps/10 seconds, 1080x1920 and 2560x1080 all
encoded under the unchanged caps and passed full decode/format/duration/audio
validation. The 720p test also checksum-verified the archive copy and confirmed
the source original remained unchanged. Related archive protection, copy-boundary,
status/concurrency tests and service vet pass. Error regressions confirm that
allocation failure, encoder crash, cancellation and timeout produce specific
public errors without exposing raw private diagnostic paths.

No private video was transferred: automatic approval review rejected the proposed
private-file transfer, and the matching synthetic reproduction made it unnecessary.
No production settings, jobs or files were changed. The bounded VM reproduction
filesystem, public runtime bundle/toolchain, synthetic clips, test binary/caches
and local helper scripts were removed after verification. A requested API build
and deployment, followed by NReading.mp4's live retry, were pending at that point.

### Efficient archive encoding with host memory protection — November 26, 2026

The selected policy restores x265's normal lookahead, reference and B-frame
buffers, retaining HEVC 10-bit, medium, CRF 18, one frame thread and no thread pool.
Each media subprocess has a 1.5 GiB data limit and 2 GiB address-space limit;
the API deployment overlay sets a 2 GiB container limit with no swap allowance.
The API, copying and encoding share that container cap. Other VPS service caps,
including FlightData's shared limits and its reduced 256 MiB service cap, are
unchanged by this deployment.

Admission requires at least 2.5 GiB host `MemAvailable` and 1.5 GiB remaining in
the container's working-set budget. During encoding and validation the worker
checks once per second: host availability below 1 GiB or container working set
above 75% cancels the subprocess. Missing memory metrics also defer work safely.
Available memory includes reclaimable filesystem cache. A pressure deferral waits
for 30 seconds of healthy admission samples before restarting from the beginning;
it preserves the original and durable job, deletes partial output, and does not
consume a failure attempt. Persisted deferrals retain the cooldown after restart.
Only one archive encoding runs at a time. CPU niceness 10, idle I/O scheduling and
child-only OOM score adjustment of at least 500 favor foreground services. These
controls reduce risk; a sufficiently abrupt host-wide spike can still reach the
kernel OOM killer before the monitor reacts.

VM tests cover host versus free-memory parsing, both pressure thresholds, metric
failure, cooldown reset and restart, durable deferral state, subprocess priority,
and actual child cancellation with partial-output deletion and original retention.
The race-enabled encoder integration passed in a two-CPU, 2 GiB/no-swap VM
container: synthetic 720x1280/30 fps/10 seconds, 1080x1920 and 2560x1080 footage
encoded under the new process caps, passed full-decode validation and checksum
verification of the original copy. The guard, cancellation, priority, status,
protection and copy-boundary regressions and service vet passed. The Swift status
label passed syntax parsing; no Apple build was run.
No private media is copied to the VM and no production memory spike is induced.

### Cached previews while archive metadata is locked — November 26, 2026

A locked encrypted archive policy previously set every listing's `hasPreview` to
false and rejected every thumbnail request, even for ordinary files with existing
adjacent caches. The regression reproduces that listing failure against the old
code, using a sealed policy envelope with an independently locked protection
domain. The corrected handlers preserve preview eligibility and use cache-only
reads when routing cannot be decrypted. Requests for locked files still fail;
missing caches return 404 without rendering originals, and protected policy
metadata stays unavailable. Original bytes remain unchanged.

The regression and related filesystem cache/invalidation, adjacent-cache,
lifecycle, archive read-only/alias, protected-record and status/concurrency checks
pass with the race detector, along with service vet, in a two-CPU, 2 GiB/no-swap
development-image container with bounded disk scratch. The fix changes only the
server; the existing app can use it after refreshing/reopening the folder.

### Isolated archive media container — November 27, 2026

The new media protocol tests cover descriptor-based access after a rename,
invalid operations/descriptors/extensions, bounded output and partial cleanup,
one active operation, disconnect cancellation and child termination, unavailable
worker without local fallback, memory deferral/cooldown, duplicate-worker
exclusion, stale socket/restart recovery and the fail-closed drain marker.
Focused tests pass with the race detector in a 1 GiB/no-swap development-image
container; all-package vet and other companion package tests pass.

Real HEVC encoding and full decode validation pass between separate containers.
The worker has no input/storage mount, network or credentials; the caller mounts
the socket directory read-only. Its actual Docker settings are 1,073,741,824-byte
memory and memory-plus-swap caps, zero CPU quota/NanoCPUs, CPU shares 128 and
128 PIDs. Final-source peak memory was 320,487,424 bytes (about 306 MiB), with no
OOM termination. The test uses a synthetic 720x1280 video; this is not a promise
that every media file fits the 1 GiB budget. Oversized work retains the original
and fails or defers without changing codec quality or lifting the cap.

The complete service suite also passes, including VM-only database workflows,
archive publication/backup, moves, Trash, protection and real-media integrations,
using pinned rclone 1.76.0. That broader API test container uses the unchanged
2 GiB API budget because legacy embedded-worker tests require 1.5 GiB admission
headroom; the isolated media container was separately verified at exactly 1 GiB.
Privileged FUSE and external compatibility tests retain their opt-in gates.
The Compose overlay validates. No production jobs, settings or services changed.
