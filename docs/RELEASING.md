# Signing and TestFlight

## Video full-screen regression correction — TestFlight 39 (November 26, 2026)

Released **0.1.0 (39)** from `d07eabc`, tagged `testflight/0.1.0-39`.
The app code matches simulator-validated `4d91955`; intervening changes are
documentation only. The viewer has one full-screen control, no nested video
navigation stack, and a cached movie owns its player independently of layout.
Paging away or closing the viewer stops playback.

- [Simulator validation](https://github.com/agawlbb/fileBeans/actions/runs/45713689672)
  passed 136 unit tests and four UI scenarios. The streamed-video full-screen
  test passed first attempt, checking one control and the same attached item
  continuing to decode through three enter/exit cycles. The existing photo
  expansion test passed on its configured retry.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/45727108140)
  passed preflight, archive/export, upload and temporary credential cleanup.
  Apple build `9f494b03-61f4-4a04-bff3-67a72bbdf539` is **VALID / IN_BETA_TESTING**,
  with automatic notification enabled. Existing tester settings were preserved.
- No server deployment, local staging or retained release artifacts; the release
  run has zero artifacts. Physical-device playback acceptance remains manual.

## Video playback — TestFlight 38 (November 25, 2026)

Device feedback found a regression: build 38 exposes two full-screen controls
and can immediately become black and unresponsive on entering full screen.
The simulator results below did not cover video presentation transitions.
Correction source `50804b3` restores the single viewer control, removes the
nested video navigation stack, and gives the cached movie ownership of its
player. The new video UI regression checks control count, item identity and
continued decoding through repeated transitions. This correction is released
in TestFlight 39 above. [Simulator-only validation
45713689672](https://github.com/agawlbb/fileBeans/actions/runs/45713689672)
passed for `4d91955`: 136 unit tests and four UI scenarios, including the new
video control-count/item-identity/decoding test on its first attempt. The existing
photo expansion test required its configured retry. The preceding run
45712414768 stopped before compilation because the shared fixture had a duplicate
generated project group; `4d91955` corrects the grouping. Both runs' disposable
result artifacts were removed after recording these results. That correction
task ran simulator tests only; the separately requested release is recorded above.

Release source `40f5839` attempted to preserve playback with a native player
controller, extended the full-screen black background through the safe area,
and added recovery for buffered waiting playback that preserves intentional pause.

- [Apple validation](https://github.com/agawlbb/fileBeans/actions/runs/45707728126)
  passed 135 unit tests, 3 UI tests and the unsigned iPhone/iPad Release archive.
  Playback tests include repeated forward/backward seeks with decoded-frame and
  clock advancement checks, plus intentional-pause and final cleanup assertions.
- [Signed upload](https://github.com/agawlbb/fileBeans/actions/runs/45710373350)
  passed signing preflight, archive/export, upload and runner credential cleanup.
  Apple build `a3eb10cd-ab4d-444a-9ea6-e74104a4aa8a`, version **0.1.0 (38)**,
  is **VALID / IN_BETA_TESTING**. Automatic notification is enabled;
  the existing internal group retains access to all builds.
- Disposable validation artifacts were deleted after success; no local build,
  signing or staging artifacts were created. No server deployment was needed.

Physical-device acceptance remains manual: verify the bottom edge, repeated
seek/full-screen transitions and automatic buffering recovery on iPhone/iPad
using the scenarios in [TESTING.md](TESTING.md). The automated results do not
establish that every reported device symptom is resolved.


## Stage-three linked archives — server and TestFlight 37 (November 25, 2026)

Deployed server source `4ce7561336e447da65abc458b8021024ad5c0911` as
`filebeans-api:4ce7561-archives`. New media waits five minutes after its last
completed upload, move, rename or observed modification. The established
photo/video profiles produce validated temporary output before replacing the VPS
copy. Exact originals remain in protected hidden VPS staging until checksum-verified
archive and thumbnail publication; offline retries occur after 1, 5, 15 and then
30 minutes. The app alerts on launch/foreground when archive work is pending.

FileBeans moves and renames within the Library, including nested/empty folders,
propagate to the archive while retaining originals' extensions. Trash and restore
propagate through a durable operation journal, preserving Samba's ten-day retention.
Archive folders remain read-only and thumbnails are served from caches without
opening originals for generation. Moving out of a Library and in-place media
replacement remain blocked. External drift never authorizes deleting originals.

- Full VM Go race tests and vet passed, including real photo/video encoding,
  offline publication, retained originals/retries, restart/crash recovery,
  conflicts, nested moves, Trash/restore, path reuse, encrypted routing and drift.
  The final correction adds a passing extension-only rename regression: changing
  `photo.jpg` to `photo.jpeg` leaves the archive original at `photo.png`.
  The isolated local/Samba FUSE suite passed for the unchanged protection layer;
  its synthetic SMB transport skips the unsupported hardlink-upload subtest.
- Final VM-only export: 243,868,672 bytes, SHA-256
  `30f0eec59a429521c9056daab1c1b88d055b88c465e18e0b7a55c33e2e5f292e`;
  manifest `sha256:9a47f6684cd3c6f0c8c0d0f2e28a711dfce42e8bbe36e98087c9ee84e8b4ef7d`;
  config `sha256:7b66cb319bb03167dae8c94b05d56bd0cf32700c522d25323c947f477f01c089`.
  Transfer checksums and loaded identities were verified before each cutover.
- Only the API was recreated. Broker PID **21956**, unlocked folders, other
  service identities, private configuration and 1 GiB/one-CPU limits were
  preserved. No normalizer/broker restart, global lock or schema migration
  occurred. The first correction cutover automatically rolled back after startup
  restarts; a fresh cutover passed with **zero restarts and no OOM**. Final public
  authentication returned 200/401 as expected; archive writes returned 403 naming
  `VPS /pics`; pending originals/changes/problems were all zero.
- The initial rollout `a0e02e4` passed the complete live synthetic round trip:
  a 179-byte PNG remained unchanged during the real five-minute delay, became a
  VPS JPEG, and its archived original matched the upload checksum. Both cached
  thumbnails were identical. File/folder moves, Trash, ten-day archive retention,
  paired restore and restored caches passed. After removing the fixture, a
  names-only audit reconfirmed **742 files on each source**, matched without
  ambiguous stems. Original media and the original tar.gz were preserved.
- The final correction additionally passed a focused protected live check:
  extension-only rename preserved the archive original's path, inode, size,
  timestamp and checksum, with identical cached thumbnails and a clear queue.
  Both final synthetic folders were removed under a protection lease.
- VM `.build/phase3` and `.build/phase3-fix`, their bounded filesystem backing
  files, exports, contexts, compiler/toolchain caches, fixture controls and
  temporary builder state were removed; the shared builder cache was restored.
  VPS `.phase3-a0e02e4`, `.phase3-4ce7561`, next-overlay staging, disposable
  verification containers/log attachment and superseded API images were removed.
  No VM/VPS task artifact remains; the current image is production state.

Released **0.1.0 (37)** from `f3624f167812b9a1c7898ad9c3a3c9b0db189c2e`,
pinned by `testflight/0.1.0-37`. The subsequent server correction changes no Apple
application, test, project or workflow inputs.

- [Validation 45678677939](https://github.com/agawlbb/fileBeans/actions/runs/45678677939)
  passed **135 unit and 3 UI tests**, zero failures, and the unsigned iPhone/iPad
  Release archive. Earlier validation 45671244822 passed all unit tests on both
  attempts but encountered Xcode launch failures before the first UI interaction;
  the other two UI tests passed. Validation now permits one bounded built-in
  retry per failed test, using [Apple's test repetition support](https://developer.apple.com/documentation/Xcode-Release-Notes/xcode-13-release-notes),
  with the same assertions and result bundle.
- [Signed publication 45682031271](https://github.com/agawlbb/fileBeans/actions/runs/45682031271)
  passed preflight, archive/export, upload with no errors and temporary runner
  credential cleanup. Apple build `c76f02e1-214b-4abc-a5d0-9e372b57c4bd` is
  **VALID / IN_BETA_TESTING**, automatic notification enabled. Existing audience
  and permanent signing credentials were preserved; no external review was
  submitted. No Apple artifacts were downloaded to the VM or VPS; GitHub's
  validation artifacts retain the existing seven-day policy.

The requested implementation, server deployment and internal app release are
finished and verified. No further deployment or app build is needed.
Physical-device acceptance remains separate.


## Stage-two linked archives — server and TestFlight 36 (November 25, 2026)

Deployed server source `30523c31a3f6ba00f5d43a2b919674598a929a3b` as
`filebeans-api:30523c3-backfill`. Archive roots and descendants are read-only,
with errors naming the matched Library; previews serve existing archive caches
without generating thumbnails or opening archive originals. Normal thumbnail
generation outside archives remains enabled. Automatic copying/transcoding and
propagation of Library edits remain deferred.

- The missing-thumbnail investigation found generation previously occurred only
  when browsing. The elected API worker now backfills VPS sources automatically
  after startup and every 15 minutes, prioritizing linked libraries. It reuses
  current caches, processes missing previews serially through existing renderer
  limits, and skips Samba, archives, locked folders, Trash and internal paths.
- The protected VPS `/pics` cache reached **742 current thumbnails for 742 files**.
  The completed cache-only Samba import verified all **742 thumbnails**, with
  zero missing/invalid caches. Its resumed final pass copied 456 JPEGs
  (6,210,137 bytes) and verified 286 already identical. Both trees match by
  relative path and filename stem, ignoring the encoding extension; original
  media and the original tar.gz are preserved. Four atomic transfers at a time
  stay within the existing memory/process and 128 MiB copy limits; all eight
  importer regressions passed, including parallel budget enforcement.
- Full Go race tests with the VM-only database and vet passed. A first database
  run hit the test user's four-connection quota; the rerun passed. New backfill
  regressions cover nested generation, cached unreadable originals, cancellation,
  symlink/Trash/internal exclusion and zero Samba generation.
- VM-built archive: 243,800,576 bytes; SHA-256
  `890c514071956f8368655c37b12c38a8a63382fd7e5f6f2b081e810baf4aa644`.
  Manifest `sha256:6f86215daf27437cc27aba2f3db235092ea4249bb7ed3e22b3d929fecb8130f6`;
  config `sha256:77b881d7d6df9820ab54cc0520e37c23dbc404d8ef9e570840a09d61406b3e35`.
  Transfer checksums and loaded image identities were verified before cutover.
- Only the API was recreated, with no build/pull/dependency restart. Private
  configuration, other container identities and broker PID **21956** were
  preserved. No global lock or normalizer restart occurred. Live archive listing,
  cached JPEG retrieval, read-only rejection naming `VPS /pics`, authenticated
  public HTTPS 200 and anonymous 401 passed; API restarts zero, no OOM.
- VPS transfer directories/next-overlay files and both superseded API images
  were removed. VM image exports, prepared binaries, toolchain/cache, temporary
  builder state and the bounded scratch filesystem/backing file were removed;
  the shared builder cache mount was restored.

Released **0.1.0 (36)** from `29719170a929baf9b15dbdc679e232bda83adc84`,
pinned by `testflight/0.1.0-36`.

- [Apple validation 45549423204](https://github.com/agawlbb/fileBeans/actions/runs/45549423204)
  passed **134 unit and 3 UI tests**, zero failures, and the unsigned iPhone/iPad
  Release archive. Its source `703f05d` has identical Apple application, tests,
  project and workflow inputs to the release; the subsequent commit changes only
  the standalone thumbnail import tool/tests.
- Initial validation 45546862498 passed the archive transport/metadata and three
  UI tests but exposed an existing viewer-cache test's two-second disk wait.
  Commit `703f05d` extends only that test wait; the final validation passed.
- [Signed publication 45551465554](https://github.com/agawlbb/fileBeans/actions/runs/45551465554)
  passed preflight, archive/export, upload with no errors, and temporary runner
  credential cleanup. Apple build `2d3b1738-bc13-4af4-ae62-7a507670d8cd` is
  **VALID / IN_BETA_TESTING**, with automatic internal notification enabled.
  Existing audience and permanent signing credentials were preserved; no external
  review was submitted. Runner validation artifacts retain the existing seven-day
  policy; no Apple artifacts were downloaded to the VM or VPS.

The requested deployment, thumbnail reconciliation and internal app release are
finished and verified. No further deployment or app build is needed. Physical-device
acceptance remains separate. The previously pending public-hosting API source is
also included in this main-branch deployment; no hosting links were created by
this operation.


## Protected Samba listing — server hotfix (November 25, 2026)

Deployed `filebeans-api:5d38260-listing`: the running source
`66e17a84b4a5f2e12378ddcf5ced994fe90ddfec` plus only the approved listing patch
from `5d38260`. The patch pins an already-authorized protected directory under
its existing request lease and obtains confined child metadata without repeating
global boundary classification for every entry. Ordinary folder checks remain
unchanged, child symlinks are excluded, and canceled listings stop promptly.

- Exact deployment-source filesystem race checks passed, including the pinned
  directory replacement/symlink regression. Existing renderer fixtures were
  included in the temporary source export. No schema migration was needed.
- VM-only build and checksum-verified transfer: 243,767,808 bytes, SHA-256
  `bbe3f8f97d3bc68ffeccac3d21285a2184a0bbf88042b670ede9c60cd0e64e8a`.
  Loaded manifest
  `sha256:d39873843172dcd93dce34da70f5001bb9919a7a130ad720008112b65c49b98f`;
  image config
  `sha256:a245785a344615e9e96ba48cba87a39673384552f31bde9379e1c62233e1f5da`.
- Only the API container was recreated with no build, pull, or dependency
  restart. Private configuration and runtime settings were identical; broker
  PID 21956, all other container identities, and unlocked protection state were
  preserved. API restart count zero, no OOM, no processing worker started.
- Live Samba/pics listing improved from 48.41 to **1.900 seconds**, returning all
  49 immediate entries (11 directories). A nested folder returned 51 entries in
  3.948 seconds; VPS/pics returned 49 entries in 0.526 seconds. Public HTTPS
  listing/authentication and restricted-path checks passed; anonymous API 401.
- The extraction wrapper had already been corrected using Pi-local renames;
  743 files / 45,032,055,965 bytes were preserved and the original tar.gz remains.
  The temporary HA helper was uninstalled, its two task directories removed,
  and the user-supplied recovery file and dedicated SSH credential preserved.
- VPS transfer staging and the unreferenced previous API image were removed.
  VM export, build context, source export, toolchain/cache and bounded scratch
  filesystem/backing file were removed. Deployment-specific builder layers and
  large context cache were removed by exact IDs; unrelated/shared cache remains.

The requested server fix is deployed and verified. No app build is required.
The separate phase-two comparison, thumbnail transfer/display and archive
read-only work remains planned; this deployment does not implement it.


## Downloads and public hosting controls — TestFlight 35 (November 24, 2026)

Released **0.1.0 (35)** from source
`ce6b8ad56416623b97f9ff84790499d23e32a7a2`, pinned by
`testflight/0.1.0-35`. Includes single/multiple file downloads, hosting controls
with expiry/indefinite duration and Settings management, protected-storage
hosting exclusion, and the pending control-free full-screen viewer change.

- [Apple validation 45454189648](https://github.com/agawlbb/fileBeans/actions/runs/45454189648)
  passed **132 unit tests and 3 UI tests**, zero failures, and the unsigned
  iPhone/iPad Release archive on Xcode 27.2. This includes both hosting transport
  regressions and the updated full-screen gesture checks.
- [Signed publication 45456586281](https://github.com/agawlbb/fileBeans/actions/runs/45456586281)
  passed signing preflight, archive/export, TestFlight upload with no errors,
  and temporary signing credential cleanup. The first release job remained
  unassigned in the larger runner pool and was canceled before execution;
  publication used the standard Xcode 27 pool and the same validated source.
- Apple build `c485c020-ae99-4510-890e-d88753e2361b` reports **VALID /
  IN_BETA_TESTING**, with automatic internal notifications enabled. Existing
  internal group access to all builds was preserved. No tester/group changes
  or external review were submitted; permanent signing credentials were preserved.

The requested internal TestFlight app release is finished and verified. No local
Apple artifacts or scratch files were created. Runner credentials were cleaned;
validation artifacts use the existing seven-day retention. Physical-device
acceptance remains under [TESTING.md](TESTING.md).

**Hosting API deployment remains pending an explicit request.** The app hides
Host File until the server advertises `publicHostingV1`; hosting downloads and
management require the updated API. This app release did not deploy or restart
any server. No additional app build is needed for the included features.

## Linked archives — server and TestFlight 34 (November 24, 2026)

Released **0.1.0 (34)** from app source
`d5bcbe9bae9920683ff3625b4600045c62a90b86`, pinned by
`testflight/0.1.0-34`.

- [Apple validation 45408821595](https://github.com/agawlbb/fileBeans/actions/runs/45408821595)
  passed **130 unit tests and 3 UI tests**, zero failures, and the unsigned
  iPhone/iPad Release archive on the Xcode 27.2 runner.
- Validation exposed a full-screen expansion defect before publication. Safe-area
  handling now belongs to the stable viewer container and pages, and the image
  scroll view does not add duplicate UIKit insets. Expansion, paging, selection
  retention and zoom/swipe regressions pass without recreating the player/cache.
  The debug fixture uses a height-limited photo for the expansion assertion and
  waits for layout. The initial stalled simulator run was cancelled; the final
  successful run used a fresh larger runner.
- [Signed publication 45410262813](https://github.com/agawlbb/fileBeans/actions/runs/45410262813)
  passed signing preflight, archive/export, TestFlight upload with no errors, and
  temporary runner credential cleanup. Apple build
  `7a099adc-bf25-490e-b4bc-f81485d6e546` is **VALID / IN_BETA_TESTING**,
  with automatic internal notifications enabled. The existing internal group
  retains access to all builds; no tester/group changes or external review were
  submitted. Permanent signing credentials were preserved.

Deployed server source `66e17a84b4a5f2e12378ddcf5ced994fe90ddfec` as
`filebeans-api:66e17a8-archives`. The API advertises `archiveLinksV1` and
`filenameStemsV1`. Links cover complete folder trees, archive thumbnail requests
stop before media reads, and linked roots require unlinking before deletion.
No copying, transcoding or processing worker was enabled by this deployment.
The dataset renderer retains its approved generated media/caption exception.

- Built on the VM in a temporary 3 GiB disk-backed filesystem, using the local
  bounded image builder. The upstream dependency download returned HTTP 403;
  reused deployed static rclone 1.76.0, verifying its SHA-256
  `62e4d3e5432f2c685f1834b8bb342058ff0611c99d048aeb44bb8d8fd611527e`.
- Export SHA-256:
  `e21de6a133243b70e6b870567bb30c9f5a2d92d1e9cdc548b3d7b3288d3e6bca`.
  Transfer length **243,758,080 bytes** and checksum matched before cutover.
  Loaded manifest:
  `sha256:cb4fbdbe90ab2f8865f8e8995d42319d9075b8477b074491efe47fa9b4a51b93`.
- Recreated only the companion with `--no-build --no-deps --pull never`.
  Broker PID, other container identities, private API configuration and unlocked
  protection state were preserved. No normalizer container was running or started;
  no database migration was required. API restart count is zero, without OOM.
- Tiny task-owned folders inside unlocked protected VPS and Samba folders passed
  live linking, future-subfolder inheritance, thumbnail rejection, case/extension
  conflict rejection, unlink-first deletion, rename tracking and unlink content
  preservation. Both probe folders and their Trash payloads were removed.
  Public HTTPS capabilities returned 200 authenticated and 401 anonymously.
- VM build context/export/cache/filesystem and smoke image, VPS transfer/rollback
  staging and the superseded API image were removed after live checks. No task
  artifacts are retained. Shared build tooling/caches and permanent credentials
  were preserved.

The requested server deployment and internal TestFlight release are finished and
verified. Physical-device acceptance remains under [TESTING.md](TESTING.md).
No local Apple artifacts were downloaded; validation artifacts follow the existing
seven-day workflow retention. No deployment or app build remains pending for
this release.


## Media screen wake protection — TestFlight 33 (November 22, 2026)

Released source `9bb028fc0c0abfbae14ad8935b9e046fe38073c7` as **0.1.0 (33)**,
pinned by `testflight/0.1.0-33`. Photos and videos keep the screen awake while
the viewer is active, including paused videos. Closing the viewer, selecting
a document or leaving the active scene restores normal dimming. Multiple
windows retain independent wake requests.

- [Apple validation](https://github.com/agawlbb/fileBeans/actions/runs/45017981905)
  passed **123 unit tests and 2 UI tests**, zero failures, and the unsigned
  iPhone/iPad Release archive on its second attempt. The first attempt passed
  all unit tests but one UI test timed out launching the app through Xcode;
  both UI tests passed on the fresh runner without source changes.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/45020298937)
  passed preflight, archive/export, TestFlight upload with no errors and
  temporary runner credential cleanup.
- Apple build `b4cd2cea-3e64-4752-81b6-537e63bf565c` is **VALID /
  IN_BETA_TESTING**, with automatic internal notification enabled. Existing
  tester settings were preserved; no external review was submitted.

The internal TestFlight release is finished and verified. Physical-device
acceptance remains: view photos and paused/playing videos beyond the configured
Auto-Lock interval, then close the viewer, select a document or background the
app and confirm normal dimming returns. No server deployment was needed. No
local build artifacts or temporary files were created; runner credentials were
cleaned and permanent signing credentials preserved. Validation artifacts retain
the existing seven-day workflow retention.

## Adjacent prefetch and text editing — TestFlight 32 (November 21, 2026)

Released app source `de91c439d4ee7c00fb079ce18ec00060b528afa8` as **0.1.0 (32)**.
The viewer prefetches adjacent images and up to 256 KiB of each adjacent video's
opening bytes for VPS, Samba and Drive. Cached images appear during swipes;
video playback reuses prefetched bytes without playing offscreen. The viewer
prioritizes the selected file, retains only its immediate neighbors and releases
downloads on eviction, memory pressure, dismissal or protection changes. This
release also includes creation and UTF-8 editing of `.txt` files in VPS/Samba
folders, with revision checks and discard confirmation.

- [Simulator validation](https://github.com/agawlbb/fileBeans/actions/runs/44960712154)
  passed **123 unit tests and 2 UI gesture tests**, zero failures, including the
  new neighbor reuse, eviction/cancellation and video-head reuse checks. Its
  duplicate unsigned archive step was cancelled after the signed device archive
  and export succeeded; the workflow conclusion is cancelled, while its
  simulator-test step succeeded. The retained simulator result artifact was
  downloaded with a size ceiling, SHA-256 and ZIP/result-bundle integrity checked:
  `11908a82ac625f22199638c97c2deb418cd618848f2abb2587dc2c0507b3367b`.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/44962319896)
  passed signing preflight, device archive/export, upload with no errors and
  temporary runner credential cleanup. Apple build
  `0e1c7c14-6676-41f9-96b7-cf62076da631` is **VALID / IN_BETA_TESTING**, version
  0.1.0, build 32, iOS 18 minimum. Existing internal audience and automatic
  notification settings were preserved; no external review was submitted.
- Release source is pinned by `testflight/0.1.0-32`. No server changes were
  required. Temporary result-bundle downloads were removed and absence verified;
  permanent signing credentials were preserved. The GitHub simulator artifact
  follows the existing seven-day workflow retention.

The requested internal TestFlight release is finished and verified. Mixed
photo/video/document navigation, slow-network behavior and text editing on a
physical device remain covered by the acceptance checks in [TESTING.md](TESTING.md).

## Photo display and swipe corrections — TestFlight 31 (November 21, 2026)

Released app source `02cb7239c0b4f8ca2f847cec86e99cc8417fa5ea` as **0.1.0 (31)**.
Build 29 introduced a white-screen photo regression. Build 30 restored display,
confirmed by the owner, but embedded Quick Look still consumed photo swipes.
Build 31 uses a fitted photo view that passes horizontal drags to the folder
pager, while zoomed photos retain panning and double-tap/pinch zoom.

- Remote-file eligibility now uses type information without probing a nonexistent
  local file. Loading and cleanup attach to a stable container so the downloaded
  preview survives the transition from loading to display.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/44910102053)
  passed **119 unit tests and 2 UI gesture tests**, zero failures, and the
  unsigned iPhone/iPad Release archive. The UI tests perform actual left/right
  swipes through three synthetic photos, check both folder boundaries and full
  screen continuity, and zoom/pan before resuming folder swipes. The fixture is
  DEBUG-only and excluded from Release behavior.
- Downloaded archive-artifact SHA-256 matched GitHub:
  `729f4557b742c6dddd772ec23aace0934e1c8353fc62ad40db55e1e10f65a6c2`.
  App, File Provider and Share identifiers, version 0.1.0, iOS 18 minimum and
  iPhone/iPad device families were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/44910489214)
  passed signing preflight, archive/export, upload with no errors and temporary
  runner credential cleanup. Apple build `996f7202-30e4-4528-a09f-c6dae038d084`
  is **VALID / IN_BETA_TESTING**, with automatic internal notifications enabled.
  Source is pinned by `testflight/0.1.0-31`.
- Intermediate [build 30](https://github.com/agawlbb/fileBeans/actions/runs/44908820198)
  from `d8e5815f499f1dca3142115b91a6d83421272ddf` also uploaded successfully and
  reached VALID / IN_BETA_TESTING. Source is pinned by `testflight/0.1.0-30`.
  Its earlier preview-host test needed an active window scene; after correcting
  that test, all 119 unit tests passed. The superseded validation run
  `44907624417` was stopped during its long-running unsigned archive step after
  the final build-31 validation and signed releases succeeded.

The replacement internal TestFlight release is finished and verified. No server
changes or deployment were needed. Temporary downloaded validation artifacts were
removed; permanent signing credentials were preserved. Physical-device acceptance
of mixed document/video/photo navigation remains under [viewer checks](TESTING.md#folder-viewer-swipes).


## Folder viewer swipes — TestFlight 29 (November 21, 2026)

Released app source `611c1584c6df1255903a563c77b34c65f744763f` as **0.1.0 (29)**.
The viewer pages through supported files in the current browser listing and keeps
one full-screen presentation across photos, videos and documents.

- [Apple validation](https://github.com/agawlbb/fileBeans/actions/runs/44903740923)
  passed all **117 simulator tests**, zero failures, and the unsigned iPhone/iPad
  Release archive. Downloaded artifact SHA-256 matched GitHub:
  `fec261227a93b212ed27942802c43265a96400e704e5a78dccc8eceb64e7d9d0`.
  App, File Provider and Share identifiers, version 0.1.0, iOS 18 minimum and both
  device families were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/44905157699)
  passed signing preflight, archive/export, TestFlight upload with no errors,
  and temporary runner credential cleanup.
- Apple build `e7e0753e-3ccb-4c38-835b-68175810cebc` is **VALID /
  IN_BETA_TESTING**, with automatic internal notifications enabled. Existing
  audience settings were preserved. Release source is pinned by
  `testflight/0.1.0-29`.

The requested internal TestFlight release is finished and verified. Swipe, zoom,
PDF scroll and playback interactions still require physical-device acceptance
under [Folder viewer swipes](TESTING.md#folder-viewer-swipes). No server deployment
was needed. The temporary validation archive download was removed; permanent
signing credentials were preserved.


## Resumable uploads and Apple integration — November 20, 2026

Server source `015534637d92149477a747a41a27e86267908ed9` is deployed as
`filebeans-api:0155346-uploads`. The API advertises `resumableUploadsV1`.

- Full companion Go race tests and vet passed using pinned rclone 1.76.0.
  The earlier system-rclone diagnostic failure does not occur with this runtime.
- Corrected VM-built archive SHA-256:
  `86d7710378d67a6da81ef6697c6dfbada6fff738318921173c609384d1dc62e2`.
  Loaded manifest:
  `sha256:9b2bf2665acb6f48bf2442384d3f688220e22f5ed5e400690c6bf4c15ceddf7c`.
  Packaged executable mode 0555 and transferred archive checksum were verified.
- Only `companion` was recreated with `--no-build --no-deps --pull never`.
  Direct/public authenticated capabilities and source discovery passed; anonymous
  requests returned 401. Temporary VPS and Samba probes passed chunk upload,
  saved-offset recovery, repeated completion, and HTTP 206 byte-range reads.
  Probe files were removed. Broker PID and normalizer identity/image/PID/start
  time/restart count were preserved; the API had zero restarts.
- The initial image had a nonexecutable binary after dependency preparation was
  interrupted. The deployment restored the previous healthy API before the image
  was corrected on the VM and redeployed. The unused failed image and this task's
  VPS transfer archives were removed. Historical recovery artifacts remain intact.
- [Apple validation 44719643789](https://github.com/agawlbb/fileBeans/actions/runs/44719643789)
  compiled the app and both extensions and ran 114 simulator tests. One existing
  thumbnail eviction assertion failed; the pruning throttle was corrected to
  enforce small budgets while amortizing production cache scans. Validation of
  the correction subsequently passed in
  [Apple validation 44721893541](https://github.com/agawlbb/fileBeans/actions/runs/44721893541)
  from source `3dba375b314ee99ab00e84cb0aa9ff2368e00e3a`: all **114 simulator
  tests**, zero failures, and the unsigned iPhone/iPad Release archive. Downloaded
  archive-artifact SHA-256 matched GitHub:
  `21ebd2ca95d2b734053bd15172d18c0d28368b3d43608047ef758155467dc09e`.
  App, File Provider and Share bundle IDs, version 0.1.0, iOS 18 minimum and both
  device families were verified.
- The Share bundle ID `com.agawlbb.myfiles.Share` is registered and linked to
  `group.com.agawlbb.myfiles`. Its App Store profile validates against the App
  Group, shared Keychain and retained distribution certificate, and is installed
  as `SHARE_PROFILE_BASE64`. Existing app/Files profiles and signing identity were
  reused. Invalid profiles created before the portal association was saved were
  removed.
- [Signed publication 44724703812](https://github.com/agawlbb/fileBeans/actions/runs/44724703812)
  from source `709e2c752bb61bdc9ba6966d18eed957565b0199` passed signing preflight,
  archive/export, TestFlight upload and temporary credential cleanup. The upload
  explicitly succeeded with no errors. App/build inputs match the validated
  source; the intervening commit changed only release documentation.
- Apple build `701ec9c6-2cc6-49e8-9995-57d4bc02f49f`, **0.1.0 (28)**, is
  **VALID / IN_BETA_TESTING**, with automatic internal notification enabled.
  Existing audience settings were preserved; no external review was submitted.
  Release source is pinned by `testflight/0.1.0-28`.

The requested server deployment and internal TestFlight release are finished and
verified. Physical-device acceptance of large-folder scrolling, Apple Files,
share-sheet uploads, background recovery and VPS playback remains with the owner.
Task build contexts, image transfers, inspected Apple artifacts, disposable logs
and temporary tools were removed. Historical recovery artifacts and permanent
signing credentials were preserved.

## Transfers and Drive move correction — November 19, 2026

Deployed feature source `3045cd712f7417a150c2cde1d47a30f75861ed53` as
`filebeans-api:3045cd7-drive-fix`. The API includes both main fixes merged into
`feature/makelora-datasets`, retaining the dataset feature. Drive move destination
verification tolerates metadata version changes when content checksums match;
source verification remains strict.

- The merged feature source passed the full companion Go race suite with the
  isolated VM test database and pinned rclone, Go vet, and static source checks.
- VM-built archive SHA-256:
  `a43319e0d70effa810719ac19f9c20ae2e694b8df662eff1e0e27510852e5c6d`.
  Image manifest:
  `sha256:fb46a7e9b787327a09dc5ceab6cdfcefb45e4fc70a126a39b85f6d2212946099`.
  Transferred archive checksum and loaded image identity were verified.
- Only `companion` was recreated with `--no-build --no-deps --pull never`.
  Authenticated direct/public source discovery and public capabilities passed,
  including `crossDriveMoves` and `datasetMediaV1`. API restart count was zero,
  with no OOM termination. Normalizer identity/image/PID/start/restart count,
  broker PID and private API configuration remained unchanged.
- No existing transfer was retried, and no user files were modified by live
  verification. Live transfer and physical-phone layout acceptance remain separate.

Root-only rollback snapshots, image metadata and `verification.json` are in
`/opt/services/filebeans/deploy/drive-fix-3045cd7/`. The VM-prepared archive is
retained in the feature checkout's `.build/drive-fix-api.tar` and on the VPS in
`/tmp/filebeans-drive-fix-3045cd7/api.tar`. Rollback restores the saved dataset
overlay and recreates only `companion`, preserving the normalizer and broker.

App release 27 is pinned by `testflight/0.1.0-27` at the deployed feature source.
[Apple validation 44604595445](https://github.com/agawlbb/fileBeans/actions/runs/44604595445)
passed all **110 simulator tests**, zero failures, and the unsigned iPhone/iPad
Release archive. Both downloaded artifacts matched GitHub's SHA-256 digests;
packaged app/extension identifiers, version 0.1.0, iOS 18 minimum and iPhone/iPad
support were verified. The initial larger-runner validation 44603564231 was
cancelled while queued, before compilation; the standard Xcode 27 runner then
completed validation successfully. Signing preflight 44603922467 passed.

[Signed publication 44609687761](https://github.com/agawlbb/fileBeans/actions/runs/44609687761)
passed preflight, signing, archive/export, upload and temporary credential cleanup.
The upload explicitly succeeded with no errors. Apple build
`d99d0772-f0f4-4bd8-acc1-57570aa9d501`, **0.1.0 (27)**, is **VALID /
IN_BETA_TESTING**, with automatic internal notification enabled. Existing signing
and audience settings were preserved; no external review was submitted.

The requested server deployment and internal TestFlight release are finished and
verified. Physical-device toolbar and live transfer acceptance remain separate.
The feature remains on `feature/makelora-datasets`, without merging into main.

## Protected-folder creation dialog — TestFlight 26 (2026-11-19)

Released **0.1.0 (26)** from main source
`5737c933ec6fd461f6991bfbb8e51dc5bced420a`, pinned by
`testflight/0.1.0-26`. New creation dialogs clear prior names and request IDs;
pending encryption-key setup and backup controls appear in the dialog.

- [Apple validation 44522393177](https://github.com/agawlbb/fileBeans/actions/runs/44522393177)
  passed **110 simulator tests**, zero failures, and the unsigned iPhone/iPad
  Release archive on the larger Xcode 27 runner. The initial validation
  44519540099 passed the protected-folder tests but hit the existing HEVC seek
  timeout; the unchanged source passed on retry.
- [Signed publication 44523394504](https://github.com/agawlbb/fileBeans/actions/runs/44523394504)
  passed preflight, archive/export, upload and temporary credential cleanup.
  The upload explicitly succeeded with no errors.
- Apple build `e7123bae-12bb-47fa-aeb1-a920475f1294` is **VALID /
  IN_BETA_TESTING**, with automatic internal notification enabled. Existing
  signing and audience settings were preserved; no external review was submitted.

The requested internal TestFlight release is finished and verified. No VPS
deployment was needed. Physical-device dialog acceptance remains a device check.

The public app identity is recorded in `Config/Release.json` and `Config/Project.xcconfig`:

| Setting | Value |
| --- | --- |
| App display name | fileBeans |
| App Store Connect record | FileBeans (`6835142643`) |
| Main bundle ID | `com.agawlbb.myfiles` |
| Files extension | `com.agawlbb.myfiles.FileProvider` |
| Share extension | `com.agawlbb.myfiles.Share` |
| Shared App Group | `group.com.agawlbb.myfiles` |
| Shared Keychain group | `FKR675M7PM.com.agawlbb.myfiles` |
| App Store SKU | `myfiles-ios` |
| Primary locale | English (U.S.) |

## Current setup

All three bundle IDs and their shared App Group are registered and linked. The GitHub `testflight` environment permits releases from the branch selected when dispatching the workflow. Its Apple team/bundle variables, five API/P12 credential secrets, and all three provisioning-profile secrets are configured. The reusable distribution identity expires November 15, 2027; its public certificate ID is recorded in release configuration.

The encrypted private key, P12, password and reuse helper are permanently retained in the VM's private Apple store, accessible read-only inside development. The installed `apple-signing inspect` command verifies them. **Do not delete the retained files after configuring GitHub or create a certificate for each project.** Future projects should use the shared workspace signing-reuse instructions. This repository contains no private signing material.

The App Store Connect iOS record now exists as **FileBeans**, with the registered bundle ID and SKU above. Apple rejected the original myFiles name; the device app and Files extension display **fileBeans**. The repository and VM checkout are named fileBeans. Xcode project/target names, bundle IDs, App Group and SKU retain their existing identifiers.

The three App Store profiles were generated and validated against the registered App Group, shared Keychain access, and retained distribution certificate. Apple registration and signing setup are complete.

To inspect the setup or refresh app-specific profiles, run the helper from the VM:

```bash
dev node /home/dev/projects/fileBeans/scripts/apple-setup.mjs inspect
dev node /home/dev/projects/fileBeans/scripts/apple-setup.mjs profiles
```

The second command reuses the existing distribution certificate, creates the three app-specific App Store profiles, verifies App Group/Keychain/certificate/expiry constraints, and stores them directly as `APP_PROFILE_BASE64`, `PROVIDER_PROFILE_BASE64`, and `SHARE_PROFILE_BASE64` in GitHub. It does not issue certificates, build apps, upload packages, invite testers or change beta groups.

## Environment settings

| Variable | Purpose |
| --- | --- |
| `APPLE_TEAM_ID` | Registered Apple team |
| `BUNDLE_ID_PREFIX` | Main bundle identifier |
| `APPLE_DISTRIBUTION_CERTIFICATE_ID` | Reusable distribution certificate metadata |

| Secret | Purpose |
| --- | --- |
| `DISTRIBUTION_P12_BASE64` | Encrypted distribution certificate/private-key package |
| `DISTRIBUTION_P12_PASSWORD` | Package password |
| `ASC_PRIVATE_KEY_BASE64` | Existing App Store Connect API key |
| `ASC_KEY_ID`, `ASC_ISSUER_ID` | API authentication metadata |
| `APP_PROFILE_BASE64`, `PROVIDER_PROFILE_BASE64` | Main app and extension App Store profiles |

When refreshing credentials for this project, the shared VM helper installs the retained identity directly into the `testflight` environment. There is no workflow-based extraction from other repositories and no credential artifact transfer. Temporary copies on ephemeral GitHub runners are removed after use; the VM originals remain intact.

## Initial build-only verification — 0.1.0 (14)

Source `b7ac1b93c30dfac816fef19e65b33c0a0144f2f6` builds the corrected single large
breadcrumb heading in place of the folder title.

- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43952665641):
  simulator tests and unsigned iPhone/iPad Release archive passed.
- [Signed build](https://github.com/agawlbb/fileBeans/actions/runs/43956876063):
  preflight, signing, archive/export, artifact retention, and credential cleanup
  passed. The TestFlight upload step was skipped (`operation=build`).
- Signed IPA artifact: `myFiles-signed-14`, retained for seven days at
  [artifact 14271014162](https://github.com/agawlbb/fileBeans/actions/runs/43956876063/artifacts/14271014162).
- Downloaded test-result, unsigned-archive, and signed-IPA artifact SHA-256 digests
  matched GitHub metadata. Both packaged targets have version 0.1.0 (14), correct
  bundle IDs, iOS 18 minimum, and iPhone/iPad support.
- This initial build skipped upload; subsequent explicitly authorized publication
  is recorded below. No VPS deployment is needed.

## File-management updates — 0.1.0 (20)

**0.1.0 (20)** is **VALID / IN_BETA_TESTING** for the existing internal
TestFlight group. Apple build ID:
`2c442a28-9e9f-419e-8b37-8920fab5bb6b`.

- Release source: `708525a25b91f2e77be3798abe6c2bbe53a7d829`, tag
  `testflight/0.1.0-20`, on `feature/encrypted-vaults`.
- [Final validation](https://github.com/agawlbb/fileBeans/actions/runs/44330888062)
  passed the simulator suite, including all eight file-operation-state tests
  and the batch-submission regression, followed by the unsigned device archive.
- [Signed publication](https://github.com/agawlbb/fileBeans/actions/runs/44336852425)
  passed signing, export, upload and temporary credential cleanup. The first
  publication dispatch 44335959946 was cancelled while waiting for a runner,
  before signing or upload.
- The app provides one flat Trash view per source, hides locked protected
  folders and their Trash entries, exposes Select/Select All for batch file
  operations, shows pending destination tiles and immediate deletion progress,
  and lists existing files in the destination picker.
- Source `05bcd79` is deployed in the companion and `f334c3a` in Quantum.
  Locked-root listing and private Trash behavior passed real local/CIFS/FUSE
  tests. Live HTTPS verified all six file/folder Trash lifecycle combinations
  across VPS, ordinary Samba and protected Samba, plus three-file copy/move
  jobs with matching contents and original retention/removal.
- The existing retained protected item migrated without a fingerprint change;
  the old public per-folder Trash directory was removed. Encrypted rollback
  metadata is in the originating vault's private area. The normalizer container
  and key-holding broker were preserved, with the existing vault unlocked.
- No schema migration, replacement encryption key, tester-group change or
  signing-identity change was required. The feature remains unmerged into main.

## Protected-folder moves and merged normalizer — November 18, 2026

**0.1.0 (19)** is available to the existing internal TestFlight group:
**VALID / IN_BETA_TESTING**, Apple build
`79db23a4-ac3b-4472-bbeb-c72e053aeec5`.

- Release source `87ae6efc9ac783a22c4c7046a189f81230898640`, tag
  `testflight/0.1.0-19`, on `feature/encrypted-vaults`.
- [Validation 44301022097](https://github.com/agawlbb/fileBeans/actions/runs/44301022097)
  passed all **87 simulator tests** and the unsigned iPhone/iPad Release archive.
  It validated `cb9fb2b`; application, extension, tests, configuration and workflow
  inputs are identical at the merged release source.
- [Upload 44305782407](https://github.com/agawlbb/fileBeans/actions/runs/44305782407)
  passed signing, export and upload. A redundant
  [dispatch 44305782427](https://github.com/agawlbb/fileBeans/actions/runs/44305782427)
  exported the same version but Apple rejected its upload as duplicate build 19.
  Its green workflow status does not establish a second upload. App Store Connect
  confirms the single available build above.
- Initial validation attempt 44298859957 passed the new move tests but timed out
  in the existing media seek test. Its queued retry was cancelled before the
  successful Intel validation above.
- `origin/main` commit `258bc7e` was merged into the feature branch as `87ae6ef`.
  The two normalizer merge conflicts retain guarded filesystem access alongside
  isolated pipeline workspaces. The feature branch has **not** been merged into
  `main`.
- VPS companion and normalizer run `87ae6ef-vaults` images; Quantum runs
  `cb9fb2b-vaults` with unchanged Quantum/protection inputs after the merge.
  The normalizer has a 4 GiB memory/swap limit, no CPU quota, and CPU shares 128.
  Quantum and broker were preserved during the normalizer upgrade; the broker
  retained PID 1637955 throughout both deployments and its vault stayed unlocked.
- Go race/MariaDB tests pass, with only the established unrelated staged-hardlink
  baseline excluded; Go vet, source checks and patched Quantum race tests pass.
  Real local FUSE/CIFS tests cover boundary-conflict responses, moves in both
  directions, durability and shared-user writes. Journal tests cover publication,
  quarantine, partial deletion, cancellation and source/destination changes.
- Authenticated live HTTPS moved disposable data into and out of the unlocked
  vault. Both moves verified content/empty directories and removed the original;
  the probe was cleaned up. No new production key or vault was created.
- All 743 normalization jobs remained accounted for. Four more completed after
  the merged rollout; three jobs were in flight at the final check. The interrupted
  encode hit the existing restart safeguard and was requeued through the supported
  retry API after the resource upgrade. Existing completed outputs were retained.
- Prepared-image checksums and rollback compose files are retained under
  `/opt/services/filebeans/deploy/moves-cb9fb2b` and
  `/opt/services/filebeans/deploy/normalizer-87ae6ef`. No schema migration,
  tester-group change or signing-identity change was needed.

## Earlier feature-branch rollout — build 18

**0.1.0 (18)** is signed, uploaded and available to the existing internal
TestFlight group. Apple reports **VALID** / **IN_BETA_TESTING**. Source remains
on `feature/encrypted-vaults`; `main` has not been merged or changed.

- Release source: `9358f30fe8d9c3a164d2f7ba07068f0565c3ce5b`, immutable tag
  `testflight/0.1.0-18`.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/44215546871)
  passed preflight, signing, archive/export, TestFlight upload and temporary
  runner credential cleanup. Apple build ID:
  `238dd3dc-e172-427a-9fe0-81849ce27c98`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/44213282822)
  passed all 83 simulator tests and the unsigned iPhone/iPad Release archive.
  Application inputs match the released source; subsequent deployment changes
  affect server code and documentation only.
- The compatible Quantum, companion, normalizer and host mount broker are now
  deployed. Authenticated HTTPS advertises `protectedFoldersCreate`; the app
  can create protected folders from the Add menu. Existing source names/paths,
  accounts, OAuth grants and queued normalization work were preserved.
- The deployment found a separate-UID normalization directory issue. Server
  source `74893ba14a83f468cae31247a12b00364eb63c03` accepts an already group-writable
  directory when an ownership-only chmod is denied. Companion race/MariaDB and
  real local/CIFS/FUSE tests pass. This server-only fix does not require another
  app build.
- No existing files were converted, no vault keys were generated on the server,
  and no production vaults were created during deployment. Physical-device key
  backup/import, biometrics, backgrounding and Files acceptance remain manual.
- No tester groups, certificates, credentials or repository protections changed.
  See [the deployed broker/runtime record](VAULT-BROKER.md#deployed-runtime--november-18-2026)
  for image digests, identities, mounts, rollback material and live checks.

## Feature-branch rollout — November 17, 2026

**0.1.0 (17)** has been signed and uploaded successfully. Apple reports
**VALID**; internal TestFlight distribution is still **PROCESSING** at the last
check. Source remains on `feature/encrypted-vaults`, unmerged into `main`.

- Companion source `9f342269f2a667300685dc251481a5f307ca6c25` is deployed as
  `filebeans-companion:9f34226`. Its archive checksum and relevant live checks
  passed; the normalizer was not recreated. See the companion deployment record.
  No vaults, storage migration or existing-file encryption were configured.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/44136905919)
  passed simulator tests and unsigned iPhone/iPad Release archiving. The downloaded
  unsigned archive and simulator result ZIPs matched GitHub SHA-256 digests
  `b2a4ea4cadd2cfa8e89203bc4d4e1232047d7f7d21d2b5d5db453436cc72a03f`
  and `660a6a42a9332e69adc57b02ba29c8a3c165841d2129c5a1df02a072f6dcc609`.
  Both packaged targets have the expected identifiers, version 0.1.0, iOS 18
  minimum and iPhone/iPad support. The unsigned archive uses default build 1;
  the signed release overrides that with build 17.
- Release source `df87e87846a76e9bc202628028c0e121015a42b2`, immutable tag
  `testflight/0.1.0-17`, adds signing checks
  for the main app's private `.vaults` Keychain group; all six release-helper
  tests pass. Its application/test/project/workflow inputs match validated
  source `9f34226`.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/44141007306)
  succeeded on retry directly from the feature branch: preflight, signed
  archive/export, TestFlight upload, and temporary runner credential cleanup
  passed. Apple build ID: `c7fe1cff-be36-47a1-8fc7-1ad825ba259b`.
  Apple recorded the upload on November 17 at 19:53:52 UTC.
- The [initial attempt](https://github.com/agawlbb/fileBeans/actions/runs/44141007306/attempts/1)
  failed during Apple's outage with HTTP 500 before signing began. On the later
  retry an intermittent `/v1/apps` HTTP 500 cleared, and the next attempt passed
  all original signing checks; none were bypassed.
- No tester groups, credentials, certificates or repository protections were
  changed. The existing internal group still has access to all builds and will
  receive this build after Apple's processing. Device acceptance for biometric key storage/import/export and vault
  controls remains manual. The server has no vaults configured yet.

## Previous published release — November 16, 2026

**0.1.0 (16)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal beta group retains access to all builds.

- Source: `84ecad127bcb62d2cc1ab18d27076a3f105c329d`, immutable tag
  `testflight/0.1.0-16`. All outstanding implementation work was merged to `main`
  before deployment and release.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43976688057):
  all **68** simulator tests and unsigned iPhone/iPad Release archiving passed,
  including queued-transfer filename/destination decoding.
- Both downloaded validation artifacts matched GitHub SHA-256 digests. Packaged
  app/extension identifiers, version 0.1.0, iOS 18 minimum, and iPhone/iPad support
  were verified. The signed workflow overrides the validation archive's default
  build number with build 16.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43978748344):
  signing preflight, signed archive/export, TestFlight upload, and temporary runner
  credential cleanup passed.
- Apple build ID: `e0be188e-e5cc-4be6-af00-3b63598cbca3`.
- Transfer controls show cancellation progress immediately, display submitted
  source/destination and filenames for new Drive jobs, and hide completed copies.
  Existing earlier submissions retain their available path/ID fallback.
- The companion was deployed first as `filebeans-companion:84ecad1`. It adds
  bounded parallel copying/discovery, directory-handle reuse, batched recovery
  updates, and fewer redundant flushes and filesystem operations. Go race/MariaDB
  tests, Go vet, and live storage/Drive smoke checks passed. A live 32-file Samba
  probe improved from 30.757 to 8.729 seconds (3.52x); this does not measure Drive
  bandwidth or isolate individual optimizations.
- Only the companion was recreated. The video normalizer's container and
  `2026-11-16T17:10:17.690121031Z` start time remained unchanged; its conversion
  continued progressing. No schema migration was required.
- Physical-device transfer controls and layout acceptance remain manual.

## Previous release — 0.1.0 (15)

**0.1.0 (15)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `952950a8128d059821907bf65b97c93569a73e4f`, immutable tag
  `testflight/0.1.0-15`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43960236333):
  simulator tests and unsigned iPhone/iPad Release archiving passed.
- Both downloaded validation artifacts matched GitHub SHA-256 digests. Packaged
  app/extension identifiers, version 0.1.0, iOS 18 minimum, and iPhone/iPad support
  were verified.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43964841908):
  preflight, signed archive/export, upload, and temporary credential cleanup passed.
- Apple build ID: `2eda1c7b-19bb-4194-9541-e6cdd548f07f`.
- The heading uses smaller path text and keeps Sources directly visible and
  tappable at every width. While browsing on iPad, the sidebar shows only
  Sources → selected source → folders; tapping Sources returns to the picker.
- Physical-device layout acceptance remains manual. No VPS deployment is needed.

## Previous release — 0.1.0 (14)

**0.1.0 (14)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `b7ac1b93c30dfac816fef19e65b33c0a0144f2f6`, immutable tag
  `testflight/0.1.0-14`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43952665641):
  simulator tests and unsigned iPhone/iPad Release archiving passed.
- Initial signed IPA build and validation artifacts were downloaded and verified
  against GitHub SHA-256 digests. Both targets have the expected identifiers,
  version 0.1.0 (14), iOS 18 minimum, and iPhone/iPad support.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43957926938):
  preflight, signed archive/export, TestFlight upload, and credential cleanup passed.
- Apple build ID: `5e839cb5-480c-4c26-b50f-2916caadd20a`.
- The folder hierarchy replaces the original folder-title heading in one large
  row below the navigation controls. Narrow layouts abbreviate the hierarchy
  while retaining source access and a menu of ancestors. The iPad sidebar retains
  its active hierarchy.
- Physical-device layout acceptance remains manual. No VPS deployment is needed.

## Previous release — 0.1.0 (13)

**0.1.0 (13)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `f5120ad25c36026f692a5b264d2fe1f16fa132c8`, immutable tag
  `testflight/0.1.0-13`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43946193403):
  simulator tests and unsigned iPhone/iPad Release archiving passed. Both source
  navigation lifecycle tests and all three breadcrumb tests passed.
- Downloaded test-result and unsigned-archive checksums matched GitHub's SHA-256
  digests. Packaged app/extension identifiers, version 0.1.0, iOS 18 minimum,
  and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43949614169):
  signing preflight, archive/export, TestFlight upload and credential cleanup passed.
- Apple build ID: `f200a7a6-7d86-4e41-88d6-4175fbca43d9`.
- Compact source navigation uses a stable stack. Folder titles show tappable
  ancestor breadcrumbs, abbreviating to a source and hierarchy menu when space
  is limited. The iPad sidebar also displays the current folder hierarchy.
- Physical-device layout, live-source switching, and iPad sidebar acceptance
  remain manual. No VPS deployment is needed for this app-only release.

## Previous release — 0.1.0 (12)

**0.1.0 (12)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `de85cab006f2a7c2a13a4b6dd962f6daaf5833b3`, immutable tag
  `testflight/0.1.0-12`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43935825811):
  simulator tests and the unsigned iPhone/iPad Release archive passed.
- Downloaded test-result and unsigned-archive SHA-256 checksums matched GitHub's
  artifact digests. Packaged app/extension identifiers, version 0.1.0,
  iOS 18 minimum, and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43939202252):
  preflight, archive/export, TestFlight upload and temporary credential cleanup
  passed on the Intel macOS runner.
- Apple build ID: `30dec7f5-5834-4d86-a550-79fa6348edc2`.
- Each source opening creates a fresh detail view/task lifetime. Returning to the
  compact source list clears selection, cancelled listings do not present errors,
  and Drive waits for a successful status check before offering sign-in.
- Physical iPhone/iPad back/reopen and source-switching acceptance remains manual.
  This release changes only the app; no VPS deployment is needed.

## Previous release — 0.1.0 (11)

**0.1.0 (11)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds. No tester/group changes
or App Store submission were made.

- Source: `f8957caf4189cf920f3744e8c7d6732071132487`, immutable tag
  `testflight/0.1.0-11`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43929950557):
  all **62** simulator tests passed, including source aggregation and persistent
  thumbnail caching; the unsigned iPhone/iPad Release archive passed.
- The downloaded test-result and unsigned-archive artifact checksums matched
  GitHub's SHA-256 digests. Packaged app/extension identifiers, version 0.1.0,
  iOS 18 minimum, and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43932404308):
  preflight, archive/export, TestFlight upload and temporary credential cleanup
  passed on the supported Intel macOS runner.
- Apple build ID: `cf1fb6c3-67e7-49e1-9afc-0e000507656c`.
- The home page now lists all connections' sources, with connection management
  in Settings and connection subtitles when multiple connections exist.
  Thumbnails persist locally with a 200 MB disk limit. Videos use native AVKit
  controls for full screen playback while preserving the player and stream.
- Physical iPhone/iPad navigation and full screen transition checks remain manual
  acceptance. This release changes only the app; no VPS deployment was needed.

## Previous release — 0.1.0 (10)

**0.1.0 (10)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access. No tester/group changes or App Store
submission were made.

- Source: `7ebd7035526d04f4ba48ebf670a80c6027ba0e78`, immutable tag
  `testflight/0.1.0-10`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43912220792):
  50 simulator tests passed, including HEVC Main10 native playback/seek and
  normalization controls; the unsigned iPhone/iPad Release archive passed.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43916695794):
  signing preflight, archive/export, upload and temporary credential cleanup passed.
- Apple build ID: `77e5818b-ca6a-4e0a-ae39-0ea73622b0d0`.
- The companion and separate FFmpeg worker run the same source on the VPS.
  **Transfers → Media Normalization** exposes job/batch progress, cancel and retry.
  Drive imports download, convert, validate, publish and clean one file at a time.
  New stable VPS `/pics` media is normalized automatically; existing VPS Trash
  and backup policies continue. This feature does not mount or modify Samba.
- The photo pilot completed. The HD video pilot is converting under a separate
  1536 MiB/one-CPU worker limit. A bounded one-time server gate will queue the
  initial Drive library only after both pilots complete and publish; failures
  leave the full import stopped. Full population is background work, not complete
  at release time. Physical iPad quality comparison remains manual.

## Previous release — 0.1.0 (9)

**0.1.0 (9)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds; no tester/group changes
or App Store submission were made.

- Release source: `7772ca4`, immutable tag `testflight/0.1.0-9`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43877396959):
  all 46 simulator tests passed, including Drive permission/empty-page/sort tests,
  and the unsigned iPhone/iPad Release archive passed. Validated source `a95aa5a`
  has identical App/Shared/Tests/FileProvider/project/workflow inputs to the release;
  the subsequent change affects only server account-reconnection recovery/docs.
- [Signed TestFlight upload](https://github.com/agawlbb/fileBeans/actions/runs/43880475777):
  signing preflight, archive/export, upload, and runner credential cleanup passed.
- Apple build ID: `ac2ec1c2-7883-42a9-8634-378ecf5aa991`.
- The VPS companion runs source `7772ca4`. Go race/MariaDB recovery tests and live
  copy/trash/Drive-browsing smoke checks passed; live Drive writes require fresh
  full-access consent and remain part of device acceptance.

After updating, open the connection's **Google Drive → Upgrade Drive Access** and
approve full Drive permission. A disconnected account instead shows **Connect
Drive**. MakeLORA's separate token is unchanged. Google Drive uses the same
list/grid browser and supports native file management, server-side copies,
Files/Photos uploads, thumbnails, media playback, and native trash. Google-native
documents open in Google's browser; server export/conversion remains deferred.
Drive stays outside Apple Files. Trash shortcuts are separate bottom bars only
at each source's top level, with no combined Trash source.

For an unconfigured connection, set companion URL
`https://files.coffeybean.com/companion` and its separate token from the private
VPS connection file or the existing Samba `fileBeans/vps_exchange` copy. Never commit or log it.
The existing OAuth client and registered callback remain configured. Physical
device acceptance, real Google write operations and codec compatibility remain
manual checks after sign-in; simulator success does not claim those checks.

### Previous release

**0.1.0 (8)** was `VALID` / `IN_BETA_TESTING`, Apple build ID
`947e98db-3f9c-4436-a491-3e1fe6e45dd6`, source `ab60f7d`, tag
`testflight/0.1.0-8`. Its [validation](https://github.com/agawlbb/fileBeans/actions/runs/43853796671)
passed 40 tests and device archiving; its [upload](https://github.com/agawlbb/fileBeans/actions/runs/43858523541)
succeeded. One stale branch-based request
[`43856976402`](https://github.com/agawlbb/fileBeans/actions/runs/43856976402)
eventually ran and failed preflight because build 8 already existed. Signing and
upload were skipped; it did not produce a second release. Do not rerun it.

## Manual workflow

Initial build verification on November 15, 2026: source `e335203` passed all 19 tests and produced an unsigned device Release archive for version **0.1.0 (1)**. The [successful run](https://github.com/agawlbb/fileBeans/actions/runs/43734190564) includes the archive artifact. Its downloaded artifact digest matched GitHub, and both bundle identifiers, the embedded Files extension, iOS 18 minimum, and iPhone/iPad support were verified. The archive SHA-256 is `b2fd58c41e00a24c64a61c4358b58b91b1c8853814f9735c497748d2fcfd5876`. It has no distribution signature and has not been uploaded to TestFlight.

The TestFlight workflow has three explicit operations:

- **preflight** (default): Ubuntu-only credential, profile, App Group, Keychain, app-record and certificate checks. No build or upload.
- **build**: after preflight passes, create a signed archive/IPA on GitHub macOS and retain the IPA for seven days. No upload.
- **upload**: create the signed package and upload it to TestFlight. Requires release authorization; existing beta-group settings may distribute the uploaded build.

Select the source branch in the workflow’s **Run workflow** menu for any operation; merging into `main` is not required.

A custom positive build number is optional; otherwise the workflow run number is used. An already-uploaded number is rejected. Run Validate iOS and the [device acceptance checks](TESTING.md) before an authorized release. The workflow does not configure testers, submit an App Store release, or change tester groups.

The workflows select stable Xcode 27.2 on GitHub's `xcode-27` Apple Silicon
runner ([installed toolchains](https://github.com/actions/runner-images/blob/main/images/macos/xcode-27-arm64-Readme.md)).
Both workflows verify that the selected device and simulator SDKs are version 27
or later before compiling. The deployment target remains iOS 18+. Physical-device acceptance remains manual. Verify privacy answers, the privacy manifest and encryption declaration against the final shipped behavior before release.

Both workflows accept the `xcode-27` and `xcode-27-xlarge` Apple Silicon runner
pools. Both select Xcode 27.2 explicitly; simulator tests precede release.

Validation selects an available iPhone runtime matching the selected Xcode SDK
(or its newest compatible installed runtime). Simulator boot has a separate
ten-minute limit; compilation/tests have twenty minutes. This keeps fresh Intel
runner setup from consuming the entire test execution budget.

### Protected-folder inactivity deployment (2026-11-18)

Broker source `70346d4362353a4998442ddca72f0d8278c753a4` is deployed with a
fixed one-hour inactivity timer. VM-built Linux/amd64 broker SHA-256:
`dc51ce708fdd9298b9580d41f52b9c8778549d9138c44f932a65adf3ebc33534`.
The running executable hash and startup policy message were verified, and the
public companion vault/trash APIs responded successfully. The deployment safely
locked the existing protected folder before restarting the broker; unlock it
from the current app. RuntimeDirectoryPreserve=restart retains the runtime
directory used by consumer binds. The existing protected-stack systemd dependency
also restarted the consumers with their existing images: companion 05bcd79,
Quantum f334c3a, normalizer 87ae6ef. No app build or release was made.

Validation: companion race suite (the documented unrelated staged-hardlink
baseline remains excluded), Go vet, and the isolated FUSE suite passed. New local
and Samba-backed idle tests advance the clock through the exact one-hour boundary,
verify reads extend the timer, exclude directory polling/private cache writes,
defer while a client holds a lease, retry successfully, and confirm recovered
bytes after unlocking. The production hour was not shortened for testing.


### Parallel normalizer deployment (2026-11-18)

Normalizer source `fed272d19f2bac77957172b43163ac2f36041aaa` was built on
VM-laptop and deployed as `filebeans-normalizer:fed272d-parallel`. Current main
contains the deployed encrypted-vault integration; no feature checkout or app
build was needed. Only the normalizer image reference changed in production
Compose, and only that service was recreated with `--no-build --no-deps`.

- Two simultaneous HEVC encoders were observed, both using two frame threads,
  medium/CRF 18 and Main 10. Separate progress readings confirmed advancement;
  one observed video completed full validation/publication during verification.
- The worker retains a 4 GiB memory limit, 4 GiB memory-plus-swap limit (no swap),
  uncapped CPU and CPU shares 128. Early live readings showed about 0.55–0.70 GiB
  memory use and 313–379% CPU, with zero restarts or OOM termination. The existing
  3.25/3.5 GiB admission/fallback thresholds remain unchanged.
- All 743 job IDs were retained. All 449 pre-deployment completed records matched
  exactly, and their 449 output files retained size, modification time and inode.
  Three interrupted jobs were cancelled/retried through supported API controls,
  retaining their IDs, so startup recovery did not serialize the new pipeline.
  Final observed totals: 451 completed, four admitted/running, 288 queued.
- Companion and Quantum container IDs/start times and vault broker PID 2104738
  were unchanged. All unrelated containers initially matched the preflight
  snapshot; flightdata-service-1 separately restarted later during verification.
  The deployment commands targeted only the normalizer.
- Focused normalization workflow and resource/completion checks passed with the
  Go race detector before packaging. The deployed binary SHA-256 is
  `b061b446710f75bb9dd0cb6622bfe2386e252668792b8646d6716cb555ecf282`.
  Archive SHA-256:
  `c709162296d780d0a4ca732d1cf7371e5b0c60de945d54dceb4edac17d473d51`.
  The VPS containerd image identity is the verified OCI manifest digest
  `sha256:d034d3267539f0f88a0b5feaceac356f3f684fdfed7e870bb0c18ef6fcabfb32`.

VM artifacts remain under `.build/normalizer/fed272d19f2bac77957172b43163ac2f36041aaa/`.
The transferred archive, metadata, original Compose file, queue/output snapshots
and verification reports are retained privately under
`/opt/services/filebeans/deploy/normalizer-fed272d/`. For an authorized rollback,
change only the normalizer image to `filebeans-normalizer:87ae6ef-vaults` and
recreate only that service with `--no-build --no-deps`; preserve any later changes
to other services. No schema migration, completed-file re-encoding, broker
restart, app build or release was performed.

### Folder operations, recovery import and SDK 27 release (2026-11-18)

- App source: `8dd77d70c794aa8e659333333a3868c22b188f2d`, immutable tag
  `testflight/0.1.0-21`. A separate normalization commit subsequently landed on
  main; the app release remains pinned to this validated source.
- Signed upload: [44369352108](https://github.com/agawlbb/fileBeans/actions/runs/44369352108),
  build **0.1.0 (21)**, Apple build
  `638eeb43-78cc-4fa7-99d0-aac83139fabb`, **VALID / IN_BETA_TESTING**.
  The log reports device and simulator SDK **27.2** and upload success with no errors.
- [Apple validation 44366675017](https://github.com/agawlbb/fileBeans/actions/runs/44366675017)
  passed its simulator tests and unsigned iPhone/iPad archive steps on the same source.
  Device biometrics and transfer of an actual user key between physical devices
  remain manual acceptance; synthetic recovery-code formatting and session-import
  regressions are included in the simulator suite.
- The VPS runs companion `filebeans-companion:8dd77d7-folders` and the matching
  broker. VM image archive SHA-256:
  `7f17e27c5723096e85d658035383e3ee9737c201ed93eedb63ec7cecc3075361`.
  Manifest `sha256:03153e3eace206ade29a209342998b1a1e52d0ea944aa5fd325d6cefdf091338`.
  Broker executable hash was matched to the VM runtime manifest.
- Deployment backups and image metadata are under
  `/opt/services/filebeans/deploy/folders-8dd77d7`. The broker restart locked the
  existing protected folder; its existing key was not exported or replaced.
  Quantum and normalizer images were preserved by this deployment.
- Focused broker recovery tests, local/Samba FUSE lifecycle tests, source-scoped
  Trash API tests, and the earlier companion race/vet checks passed. Live disposable
  VPS/Samba file and folder probes verified delete, retained payload bytes, flat
  Trash listing, restore, repeated delete and permanent deletion. All probes
  were cleaned. Source Trash requests returned in approximately 15 milliseconds.
- App changes include visible batch actions, protected-root actions, full-row
  Trash navigation, source-aware Trash loading, formatting-tolerant checksummed
  recovery imports and Universal Clipboard support. No key format or deployment
  target change is required.

### Global protection and location privacy deployment (2026-11-18)

The VPS runs broker/companion/Quantum source
`ba75bb70867468a9e4a5900d88d2167ebe9c0383`. Prepared artifacts were built on
VM-laptop, checksum-verified and loaded on the VPS without building there.

- Companion: `filebeans-companion:ba75bb7-protection`, manifest
  `sha256:23948fcd825fa8e13aeee56a6a42e928ee06fd67d3dfdabcd06735fb30dcd93d`;
  archive SHA-256
  `3f93b96b939c8874905b6e1859e2c705fcce48eb771a0031fd7af978704dba5c`.
- Quantum: `filebeans-quantum:ba75bb7-protection`, manifest
  `sha256:ad86639c4985436fb8c2de39ec4ccfcec7b2631376319de9e4a2099b7c67244e`;
  archive SHA-256
  `885c0b93a00215ca895fe1ae935991b38d6fbe24a902782fa781085afee1341a`.
- Running broker executable SHA-256:
  `009a52f3bfd7f65d49217d23479e7a7c197a5bbd846e2110c94f8454bab8a409`.
- Matching consumers were restarted through the existing protected-stack unit.
  The normalizer retains `filebeans-normalizer:fed272d-parallel` and its resource
  configuration. All three containers were running with zero restart loops.
- Live authenticated HTTPS checks verified aggregate locked state, redacted
  locked-folder descriptors, global Lock and creation capability. The Quantum
  frontend returned HTTP 200. Disposable ordinary VPS/Samba file and folder checks
  verified retained Trash payloads, listing, restoration and permanent deletion;
  all probes were cleaned up. Real-key unlock remains a user-device action.

Deployment artifacts, prior broker/configuration and both prior Compose files
are retained in root-only `/opt/services/filebeans/deploy/protection-ba75bb7`.
No new plaintext registry backup was made. The existing root registry snapshot
was moved to `/var/lib/filebeans-vaults/location-backups/folders-8dd77d7.registry.json`;
its old deployment path is a symlink to that same file. This root-owned location
is configured in `locationBackups`. The first authenticated unlock migrates the
live version-1 registry and this snapshot to encrypted location metadata using
the existing key. Before that unlock, legacy location metadata remains plaintext.
After migration, do not roll back to a version-1-only broker.

Apple validation [44391292854](https://github.com/agawlbb/fileBeans/actions/runs/44391292854)
passed all **103 simulator tests**, with zero failures, and the unsigned Release
archive for iPhone/iPad. Logs verify Xcode **27.2**, device SDK **27.2** and
simulator SDK **27.2**. The source is pinned by tag `testflight/0.1.0-22`.

Signed upload [44394355285](https://github.com/agawlbb/fileBeans/actions/runs/44394355285)
completed successfully for **0.1.0 (22)** from the same tag/source. The log reports
`ARCHIVE SUCCEEDED`, `EXPORT SUCCEEDED` and `UPLOAD SUCCEEDED with no errors`,
and verifies both SDKs at **27.2**. This release includes global protection
controls, imported-key unlock, iPad navigation and encrypted location metadata.
Physical-device biometric interaction and the user's real-key migration remain
user-device acceptance checks.

Apple build `f4a55d28-c3b4-4132-9cd5-e3d4e6ee7022` is **VALID** and
**IN_BETA_TESTING**, with existing automatic internal tester notification enabled.
External state is `READY_FOR_BETA_SUBMISSION`; no tester groups or external review
settings were changed. Deployment and the authorized TestFlight release are
finished and verified.

### Standalone filesystem API trial and TestFlight 23 (2026-11-18)

This is an owner-authorized trial on `feature/filesystem-api`; main was not
merged or changed. Runtime and app source are
`8af23930b55bad98ddb2d6363bda931f0e8cca20`, pinned by `testflight/0.1.0-23`.

- [Apple validation 44407837321](https://github.com/agawlbb/fileBeans/actions/runs/44407837321)
  passed all **104 simulator tests**, with zero failures, and the unsigned device
  Release archive. Xcode/device/simulator SDK versions are **27.2**.
- [Signed upload 44410395658](https://github.com/agawlbb/fileBeans/actions/runs/44410395658)
  reports archive/export success and `UPLOAD SUCCEEDED with no errors` for
  **0.1.0 (23)**. Apple build `4cf1e3a5-5f67-40fc-8e0a-15922baf2ba6` is
  **VALID / IN_BETA_TESTING**. Existing automatic internal notification remains
  enabled; external state is `READY_FOR_BETA_SUBMISSION`. No tester groups,
  branch protections or external review settings were changed.
- The VPS runs `filebeans-api:8af2393-trial`, built on VM-laptop with independent
  Debian/FFmpeg/Poppler/rclone runtime assets. Manifest:
  `sha256:21fda110568b09ea9e8d42aced43840c13c9f507a1a820b909929f6c43ec6052`.
  Image archive SHA-256:
  `8dae096bcfe1e8b97919c227bd5d046bad1b8e7cd5143d02f07b3535f5b72d8e`.
  The transferred archive and loaded image identity were verified. The matching
  broker binary was checked against the VM artifact manifest.
- Quantum is stopped, with its image, configuration and private database retained
  for rollback. The broker's `filesystemConsumer` mode uses the API's control UID
  for readiness. Protected folders are locked following broker restart; real-key
  unlock and physical-device acceptance remain user-device checks.
- A dedicated `filesystem-trial.json` and companion-only Compose overlay leave
  the normalizer's configuration untouched. The temporary cache override for
  source `VPS`, path `/pics`, stores ordinary thumbnails/scratch in private API
  state outside the scan. Protected thumbnails still stay inside encryption.
  Remove this override after the old worker is retired; no originals need moving.
- A trial systemd drop-in excludes the normalizer from stack stop/start commands.
  The worker remains on `filebeans-normalizer:fed272d-parallel`. Container
  `a1e4d4109975`, start time, restart count and PID **2167339** stayed unchanged;
  the same FFmpeg process **2188617** survived. All **743** normalization job IDs
  and **454** previously completed records were retained. Live progress advanced
  during and after cutover; no running conversion was restarted by deployment.
- Go race/vet checks, isolated local/CIFS encrypted-folder tests and the built
  runtime's synthetic API/renderer tests passed. Live authenticated HTTPS checks
  covered source discovery, permissions, ranges, conflicts, thumbnails and
  synthetic VPS/Samba create/upload/edit/rename/Trash/restore/purge. Disposable
  probes were removed. `/pics` thumbnails were confirmed outside the watched
  tree. API restart count and OOM count were zero during verification.

VM artifacts remain in this feature worktree's
`.build/filesystem-runtime/8af23930b55bad98ddb2d6363bda931f0e8cca20/` and
`.build/filesystem-images/8af2393/`. Root-only VPS rollback files, original
configuration/broker, images, worker snapshots and deployment scripts are retained
under `/opt/services/filebeans/deploy/filesystem-8af2393/`.

For a requested rollback during the trial, the retained
`bundle/deploy.py rollback` restores the prior broker and companion/Quantum
services without issuing normalizer stop/start commands. It preserves the worker
present at rollback time. Reinstall the previous native build separately when
requested. No SQL migration or ciphertext conversion was performed.

Trial deployment and the authorized TestFlight release are finished and verified;
physical-device acceptance and the owner's permanent-merge decision remain open.


### Copy correction and temporary Samba thumbnail suspension (2026-11-18)

The owner stopped the API during diagnosis, then requested the copy fix, temporary
Samba thumbnail suspension, deployment and restart. Source
`eda5913c22be1908bee4ef924d038e6205a99c29` remains isolated on the filesystem API
feature branch. Main was not changed.

- Single-file copies no longer receive directory filter flags, which rclone
  rejects. Folder copies still exclude root/nested thumbnail caches and preserve
  empty user folders. Critical-level errors are now surfaced through redaction.
- `disabledThumbnailSources: ["Samba"]` is set in the private API configuration.
  Metadata advertises no previews for that source; thumbnail requests return
  before mount/cache/original access. Existing cache files are retained. The
  ordinary `/pics` cache override remains unchanged.
- VM Go race tests and vet passed. The exact pinned rclone exercised single-file
  copies, nested folder copies, empty folders, cache exclusions and real critical
  error output without requiring SQL. The built image passed those tests and
  ordinary API/renderer/thumbnail-policy regressions in an isolated VM container.
- Deployed `filebeans-api:eda5913-copyfix`, VM-built and checksum-verified.
  Manifest: `sha256:2afee6084e8545b7fd80201aa81d7cb6754357098268ac983acc855c5b5f1266`.
  Archive SHA-256:
  `8ffd3fb0bcca690ef2a389f59cf6fa131694c7bbee4dd8575c82598baea0df9d`.
  Only the companion/API container was recreated with `--no-build --no-deps`;
  the normalizer container identity, start time, PID, image and restart count,
  and the broker PID stayed unchanged.
- Live authenticated HTTPS and VPS-only single-file/folder copy jobs passed.
  The reported failed copy was retried only after checking its original request
  and absent destination. It completed; source metadata remained unchanged and
  source/destination content hashes matched. Synthetic probes were removed.
  No Samba files were created or modified by verification.
- Existing TestFlight **0.1.0 (23)** is compatible. No Apple build or upload was
  needed for this server-only correction.

Prepared archives, previous API configuration/overlay and worker snapshots are
retained under root-only `/opt/services/filebeans/deploy/copyfix-eda5913/`.
`verification.json` records live checks. The API is running; the worker and broker
were not restarted. This requested deployment is finished and verified.

### VPS video/PDF renderer launch correction (2026-11-18)

Source `e58aa165f3d39448e1872d50425e05e3db2c1447` corrects the subprocess
working directory without weakening the API's `PR_SET_DUMPABLE=0` policy. The
renderer now resolves its own inherited directory descriptor before Go remaps
ExtraFiles, rather than trying to access the protected parent's `/proc` entries.
Input and scratch/cache directory descriptors, broker leases, encrypted storage
and bounded renderer concurrency remain in place.

- A helper-process regression reproduced the old launch failure with process
  privacy enabled, then passed for real video and PDF previews with the fix.
  It also checks that the API/helper privacy setting remains disabled for dumps
  after rendering. Go race/vet checks and isolated local/CIFS encrypted-folder
  tests passed. The exact unprivileged runtime image passed the same regression
  and ordinary API/preview-policy checks.
- VM-built image: `filebeans-api:e58aa16-previews`. Manifest:
  `sha256:0e0426119dd5520a525d0cd133882d55aa663cd4491a9108c93de4da58c28eb9`.
  Archive SHA-256:
  `db17b3ba1c9e0f269f80fa5b15004cfe9240810aa1a875a2ea179d5908a61cd6`.
  Transfer checksum and loaded image identity were verified before cutover.
- Only the API container was recreated. Its private configuration was preserved
  byte-for-byte: Samba thumbnail generation remains disabled and ordinary `/pics`
  thumbnails/scratch still use private state outside the normalization scan.
  Worker container ID, PID, start time, image/restart count and broker PID were
  checked unchanged.
- Live checks generated JPEG previews for three existing VPS videos in distinct
  folders, verified cache files in private state, verified matching cached
  responses and confirmed original inode/size/modification time remained
  unchanged. No Samba originals were accessed by these checks. Authenticated
  HTTPS source discovery and Samba preview rejection passed.

Rollback overlay/config snapshots, the prepared image/metadata, worker snapshots
and `verification.json` are retained under root-only
`/opt/services/filebeans/deploy/previews-e58aa16/`. Source stays on the feature
branch; main was not changed. No app build or release was needed; TestFlight 23
is compatible. Reopen a video folder to start new preview requests after an
already-visible thumbnail task failed before the update.

Requested correction and API deployment are finished and verified. Remaining
VPS thumbnails are generated on demand as the owner browses; no bulk archive scan
or background thumbnail worker was started.

## Adjacent VPS and Samba thumbnail caches — November 19, 2026 UTC

At the owner's request, deployed feature source
`7d68475a5478004d24a3c02e7fbb6135b09e3809` as
`filebeans-api:7d68475-thumbnails`. Removed the obsolete
`thumbnailCacheOverrides` and `disabledThumbnailSources` keys from the private
API configuration. All folders now use adjacent `.filebeans-thumbnails` caches;
Samba previews are enabled. No app build or release was needed.

- Prepared and built the API image on VM-laptop. Manifest:
  `sha256:ba30d5a04540b779d1ae81d2e22861888fc89ce0f14947a02a28a7283f7e07af`.
  Archive SHA-256:
  `539d29515c71e6a664469ca19fc57fc06ffd53b498c0a9c042c4435640fc0ebd`.
  Verified the transferred archive and loaded image identity before cutover.
- Recreated only `companion` with `--no-build --no-deps`. The initial health
  check incorrectly used host loopback, causing automatic API rollback; the
  corrected Docker-network check passed on retry.
- Live VPS and Samba video previews produced JPEGs in adjacent hidden caches.
  Repeated responses matched cached bytes; original inode, size and modification
  time were unchanged. API listings hid cache folders. Both video sources passed
  HTTP range requests, and authenticated public HTTPS source discovery passed.
- The separately deployed `filebeans-normalizer:3644c21-recovery` container ID,
  PID, start time, image, command and restart count were unchanged across API
  deployment and verification. The broker process was also preserved. This
  deployment made no encoder or main-branch changes.

Rollback image/configuration snapshots and verification reports are retained in
root-only `/opt/services/filebeans/deploy/thumbnails-7d68475/` and
`/opt/services/filebeans/deploy/thumbnails-7d68475-retry1/`. Restore the saved API
configuration/overlay and recreate only `companion` to roll back; preserve the
normalizer and media/cache files. Thumbnails continue to generate on demand as
folders are browsed; no bulk media scan was started.


## Permanent API acceptance and Quantum retirement — November 19, 2026 UTC

The owner accepted the filesystem API after successful copies, deletes,
thumbnail generation and streaming, and explicitly requested its merge into
main, removal of the feature branch/worktree, and Quantum removal from the VPS.
Merged `feature/filesystem-api` through `bab6a65`, preserving main's encoder
recovery, picture-priority scheduling, and standalone cache exclusions. The
single scan conflict was resolved in favor of main's `walkNormalizationOriginals`
implementation. The merged backend passed Go tests with race detection and vet;
Swift/Python/plist/assets/project structure and isolation validation passed.
No app build or API/encoder image deployment was required for this acceptance.
The observed stale locked-folder listing in Apple Files is deferred at the
owner's request.

On the VPS, removed the already stopped `filebrowser-quantum` container and all
five installed Quantum image tags (`filebeans-quantum` versions and the upstream
stable image). Retired `/opt/services/filebrowser/compose.yaml` into root-only
`/opt/services/filebeans/deploy/quantum-retired-20261119/`. Preserved all media,
encrypted storage, Quantum database/configuration files and historical recovery
archives. The former Quantum proxy fallback now returns HTTP 404; the native
`/companion` proxy route is unchanged. Caddy configuration validation/reload and
authenticated HTTPS source discovery passed. API and normalizer container IDs,
images, PIDs, start times and restart counts remained unchanged. The retirement
directory retains previous proxy/Compose definitions, container metadata, and
`verification.json`.

The feature worktree's ignored `.build` artifacts are retained under the main
checkout's `.build/accepted-filesystem-worktree-artifacts/` before removal of the
worktree and merged local/remote feature branch. Historical source commits and
release records remain reachable from main.

## Settings and Files privacy corrections — TestFlight 24 (2026-11-19)

Released **0.1.0 (24)** from main source
`8f346f9464cbb8ca1ef62fb7d2c3f0badbde0ee6`, pinned by
`testflight/0.1.0-24`.

- [Validation 44441308287](https://github.com/agawlbb/fileBeans/actions/runs/44441308287)
  passed all **107 simulator tests**, with zero failures, and the unsigned
  iPhone/iPad Release archive using Xcode 27.2.
- [Signed publication 44443690658](https://github.com/agawlbb/fileBeans/actions/runs/44443690658)
  passed signing preflight, archive/export, TestFlight upload, and temporary
  credential cleanup. The upload log explicitly reports success with no errors.
- Apple build `65f242fb-d196-44c4-a6bc-e5826b3051c7` is **VALID /
  IN_BETA_TESTING**. Automatic internal notification remains enabled. The
  existing internal group retains access to all builds; no audience settings
  or signing credentials were changed. External state is
  `READY_FOR_BETA_SUBMISSION`; no external review was submitted.
- Protected folders and descendants are excluded from Apple Files even when
  unlocked, including cached identifier operations and working-set changes.
  iPad sidebar taps replace the active detail stack; photo/video upload uses
  a picker attached to the browser. Key and Drive connection tools are in
  Settings, with Lock/Unlock first. Transfers remains in folder top bars and
  moves to the Sources top bar. Protected-folder creation is hidden while locked.

The requested TestFlight release is finished and verified. No VPS deployment
was needed. Physical-device interaction checks remain in `docs/TESTING.md`.

## Sidebar, uploads, and cross-Drive moves — TestFlight 25 (2026-11-19)

Released **0.1.0 (25)** from `2d46435a6e5cd8dc993b82ab5048c939940d3607`,
pinned by `testflight/0.1.0-25`. Runtime/application changes are in `ad5650b`;
the release commit only updates test expectations for cross-provider moves.

- [Apple validation 44450075330](https://github.com/agawlbb/fileBeans/actions/runs/44450075330)
  passed **109 simulator tests**, zero failures, and the iPhone/iPad Release
  archive. The initial validation caught an outdated assertion forbidding Drive
  moves; it was corrected before publication.
- [Signed publication 44451427204](https://github.com/agawlbb/fileBeans/actions/runs/44451427204)
  passed preflight, archive/export, upload, and temporary credential cleanup.
  Apple build `3176d0ab-ffdc-48f8-a025-7b2c53c92ac1` is **VALID /
  IN_BETA_TESTING**, with automatic internal notification enabled. Existing
  audience and signing settings were preserved; no external review was submitted.
- Sidebar navigation now tracks local/Drive Trash and Media Normalization along
  with other main screens. Sources appears in the iPad detail area, and a shared
  Transfers control stays in the main-content top bar. Transfers includes local
  and Drive uploads before the server-job phase without duplicate rows.
- Cross-Drive moves use existing copy engines, revalidate source/destination,
  then persist a source-cleanup journal. Sources survive failed copies and
  pre-commit cancellation. Source changes, destination changes, lost responses,
  account identity, and restart cleanup are checked before removal.
- VM Go race tests, isolated database recovery tests, vet, and Swift/configuration
  checks passed. Deployed only `filebeans-api:ad5650b-drive-moves`, built on the
  VM with manifest
  `sha256:95c9ccb6ac03c51a2df57486333be7276eb4fd0cf63240695a2fad2b82794551`.
  Archive SHA-256:
  `5d1f325dd9b97b56005ef6c66c6caa0e1af62e70dcc5ded320175810bea61bff`.
  Transfer checksum and loaded image identity were verified before cutover.
- Authenticated HTTPS source/capability checks and Drive metadata reads passed.
  API restart count was zero, with no OOM termination. Normalizer container
  identity/image/PID/start time/restart count, broker PID, and API configuration
  remained unchanged. No encoder or broker restart or database migration occurred.

Rollback overlay/configuration snapshots and verification reports are retained
under root-only `/opt/services/filebeans/deploy/drive-moves-ad5650b/`; the image
archive remains in `/tmp/filebeans-ad5650b/` and on the VM. Restore the saved
overlay and recreate only `companion` with `--no-build --no-deps` for rollback.
Do not alter the normalizer, broker, or storage data.

The requested deployment and internal TestFlight release are finished and
verified. Physical-device acceptance remains separate. Live round-trip move
verification awaits approval after automatic review rejected the original
remote move/permanent-purge test; no connected-service data test was executed.
# Faster local moves — November 19, 2026 UTC

Deployed `d9884245de3bf3fa00eb7158c676d21bccd61e42` as
`filebeans-api:d988424-fast-moves`. Local moves use journaled no-overwrite
renames where the filesystem and protection boundary allow them. Copy-based
local moves no longer repeatedly hash source contents; preparation is visible
and hash reads honor cancellation between buffers. No app build or schema
migration was required.

The VM-prepared archive SHA-256 is
`584c599063076d13adcffb8420502e29fe3ecb9cdaae6c84f366fc71f9c69c3d`;
image config digest is
`sha256:493540047f6460ea00088a57ce2f54ff8e1d4c8e4331c561e811cf8f67af09a8`.
Transferred archive and loaded identity were verified before restarting only
the companion with `--no-build --no-deps --pull never`. The normalizer, broker
and private API configuration were preserved. Direct source discovery and
authenticated public capabilities passed. Deployment metadata, prior overlay
and source revision, original-move snapshots, and live verification are retained
under `/opt/services/filebeans/deploy/fast-moves-d988424/`.

Live Samba checks confirmed inode preservation for an ordinary move and matching
contents/source removal for moves into and out of an unlocked protected folder.
All disposable fixtures were removed. The 4 MiB return move took 72.15 seconds;
protected-boundary moves still copy and do not have rename latency. The user's
existing cancelled archive job stayed cancelled, and the original archive's
device, inode, size and modification time were unchanged by deployment.

The active-operation protection lease is unchanged: auto-lock cannot acquire its
exclusive gate while a copy/move is active, including preparation and cleanup.

## MakeLORA dataset feature trial — November 19, 2026

Deployed `filebeans-api:dc6ecd6-datasets`, `filebeans-normalizer:793eeb8-datasets`
and the broker binary prepared from `793eeb8`, after the archive-move maintenance
finished. The source remains isolated on `feature/makelora-datasets` and is not
merged into main. The newer main change affects the iPhone toolbar only.

Authenticated live checks verified `datasetMediaV1`, filesystem source discovery
and MakeLORA's connection. The broker is active; API and normalizer are running
with zero restarts. Protected folders are locked following broker restart and
can be unlocked in the existing FileBeans mobile app. The paired MakeLORA release
**0.1.0 (13)** is VALID / IN_BETA_TESTING for its existing internal audience.

VM-built archives were checksum verified before load. This VPS Docker engine
reports loaded image IDs as manifest digests: API
`sha256:3744e84a6e29d45ee2acbafb6edbcbd4e7d3ac8f825cb1f6f5403b6449255c4b`,
normalizer
`sha256:44529c73de6e7b8790db3b0b00a0ab635e1e24f37b2fc1a6df40ac14989d3143`.
The active feature overlay is
`/opt/services/filebeans/makelora-datasets.compose.yaml`.

Rollback snapshots, image archives, broker binary, MakeLORA source/gateway and
before/after service reports are retained root-only under
`/opt/services/filebeans/deploy/makelora-datasets-dc6ecd6/`. Preserve these files
and all user data. Rollback restores the saved broker and previous Compose/image
configuration, together with the MakeLORA source/gateway. No SQL migration was
required. Generation code and its existing retry timer remain unchanged.

Go race/vet checks, database-backed dataset recovery/upload tests, encrypted
local/Samba preparation and inactivity tests, and isolated runtime smoke tests
passed before deployment. Physical-device dataset acceptance follows cutover.

## Dataset original streaming deployment — November 19, 2026

Deployed `filebeans-api:b5c4c7f-originals` from feature commit `b5c4c7f`, preserving
the Drive fixes. It advertises `datasetOriginalsV1`. MakeLORA server `3fdbcf6`
uses original references, read-only inspection, and original-byte streaming.
Preparation requests are rejected and old preparation jobs cannot execute.
Encoding occurs only for explicit export and writes directly into the chosen
output destination. Existing originals and published outputs are retained.

Verification passed: full Go race suite, vet, original-byte/range/revision tests,
real FFmpeg export/recovery with original-byte preservation, and protected local
and Samba original access with locked errors and inactivity renewal. Live checks
verified the loaded manifest, authenticated capability/source discovery, HTTP 401
without authentication, and zero API restarts. Broker and normalizer were preserved.
This server change requires no FileBeans app release. Feature branches stay unmerged.

Transferred archives and the preceding MakeLORA application directory remain in
`/opt/services/filebeans/deploy/originals-b5c4c7f/` and
`private/makelora/app-originals-3fdbcf6`; automatic approval review rejected their
removal to preserve recovery evidence. Earlier trial recovery files remain intact.
## Local protected-folder write capability deployment — November 21, 2026

Broker source `18ce95a` was built on VM-laptop and installed on the VPS after
authenticated API checks found no active transfer jobs and protection was safely
locked. Only `filebeans-vault-broker.service` restarted. The companion container
identity and start time stayed unchanged, with restart count zero.

The installed and running executable SHA-256 matches the VM artifact:
`261bb9ee4da9a4fd90c675179f03c3cabf5bff000521ece21a4b93738b13c44e`.
The root-owned systemd override
`/etc/systemd/system/filebeans-vault-broker.service.d/capabilities.conf` explicitly
requests the six required ambient capabilities while preserving the existing
unit, roots dependency, runtime preservation and capability bounding set.
Systemd unit validation passed. All nine broker threads reported effective and
permitted mask `00000000002000cb`, including the previously missing `CAP_SETUID`;
the ambient set matches. Authenticated capability, filesystem source, protection,
vault and jobs endpoints returned HTTP 200 after restart.

After the owner unlocked protected folders in fileBeans, all three new gocryptfs
processes had the required effective/permitted capabilities. Live checks inside
the existing companion container as UID/GID `1000:1000` passed mkdir, file write,
read and rename in `/pics`, with directory/file modes `0770`/`0660` and ownership
`1000:1000`. Authenticated companion API checks also passed protected mkdir,
create, read, revision-checked replacement and rename. Both uniquely named probe
directories and their files were removed and their absence verified. Existing
encrypted contents were not rewritten. No iOS release or companion image rebuild
was required. The requested server deployment is finished and verified.

Local `.build/broker-18ce95a` and VPS `/var/tmp/filebeans-broker-18ce95a` staging
were removed and their absence verified. The installed broker and systemd
override are the retained runtime files; no disposable artifacts were retained.
