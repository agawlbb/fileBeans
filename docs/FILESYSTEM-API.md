# Standalone filesystem API

The owner accepted the native API as the permanent production backend on
November 19, 2026 UTC. It is merged into `main`; the former feature branch and
separate worktree have been retired. The API and app do not call Quantum.
No browser file manager is included.

Quantum's VPS container and installed images have been removed, its Compose
file retired, and its legacy web route removed. Media, encrypted storage,
Quantum database files, and historical recovery artifacts are retained. The API
continues at its existing `/companion` URL. Earlier trial and rollback records
below describe historical deployments; see `RELEASING.md` for acceptance and
retirement verification. The reported stale locked-folder listing in Apple
Files remains a deferred follow-up.

## Service and API

### Boot unit correction (November 23, 2026)

Read-only VPS inspection found `filebeans-protected-stack.service` failed:
both its base unit and `filesystem-trial.conf` drop-in referenced the retired
`/opt/services/filebrowser/compose.yaml`. The effective startup first tried to
stop Quantum, so the missing file prevented the companion startup command.

The replacement is tracked at
`companion/deploy/filebeans-protected-stack.service`. It starts and stops only
the companion, using the three Compose files recorded on the running container
(base, filesystem trial, and MakeLORA datasets). It preserves broker ordering,
timeouts and the prohibition on building or pulling images. The former separate
normalizer container is absent and must not be recreated by this unit.

Deployed with owner authorization on November 23, 2026. Installed the replacement
as `/etc/systemd/system/filebeans-protected-stack.service` and removed the obsolete
`/etc/systemd/system/filebeans-protected-stack.service.d/filesystem-trial.conf`
override. Unit SHA-256 matched the tracked source; systemd verification passed,
Compose selected only `companion`, and the reloaded unit started successfully
(`active/exited`, result `success`). It remains enabled for boot. The companion
remained running with its original November 22 start time. Authenticated HTTPS
capabilities and filesystem source discovery returned 200; anonymous source
discovery returned 401. No app/image build, staging files or rollback copies
were needed; the obsolete override was verified absent. Boot was not reboot-tested.

The existing Go companion hosts both `/v1/fs/*` and its existing job, Trash,
Drive and protection routes. There is no database schema change. The private
Bearer token authorizes all configured roots; this is a personal server, not a
multi-user permissions service. Unix permissions and verified mount availability
still apply. Match root names and visible root directories to the previous app
configuration before switching. Paths are relative to each configured root.

Enable only in the trial companion configuration:

```json
"filesystem": {
  "enabled": true,
  "ffmpeg": "/usr/bin/ffmpeg",
  "pdftoppm": "/usr/bin/pdftoppm"
}
```

The defaults for the executable paths are those shown above. Startup checks both
executables. Keep the current `protectedRuntime`, storage roots, service UID/GID,
state directory, SQL database, OAuth credentials and token. Disable the feature
or omit the block to retain companion-only behavior.

| Method and route | Behavior |
| --- | --- |
| GET `/v1/fs/sources` | Protocol version 1, sources and personal-server permissions |
| GET `/v1/fs/resources?source=…&path=…` | Metadata and direct children for folders |
| GET/HEAD `/v1/fs/content?source=…&path=…` | Original contents; HTTP ranges, ETag and conditional reads |
| POST `/v1/fs/content?source=…&path=…` | Create without replacement |
| PUT `/v1/fs/content?source=…&path=…` | Replace with a required quoted revision in `If-Match` |
| POST `/v1/fs/folders?source=…&path=…` | Create one directory without replacement |
| POST `/v1/fs/move` | JSON `source`, `from`, `to`; same-boundary no-overwrite rename |
| GET `/v1/fs/thumbnail?source=…&path=…` | Cached or generated JPEG preview |

Metadata includes `name`, full source-relative `path`, MIME `type`, `size`,
RFC3339 `modified`, opaque `revision`, `hasPreview`, and optional `children`.
Revisions derive from modification time and size, rather than hashing file
contents across Samba. PUT without a revision returns 428, stale replacement
returns 412, and name collisions return 409. API publication checks are serialized;
arbitrary external writers can still race a revision check. Copies and complex
moves use the existing durable jobs; deletions always use managed Trash.

The `crossDriveMoves` capability enables `POST /v1/jobs` with `operation: "move"`
between a local source and Google Drive. The server completes copy publication,
revalidates the source and destination, then journals source removal. Restart and
retry resume cleanup without recopying published items. Before cleanup commits,
cancellation retains the original. Drive originals use Drive Trash; local
originals use the existing move quarantine/removal journal. Native Google
documents remain unsupported for local export. Moves within Drive continue to
use its native reparent operation.

Transfers stream through the service without full-file buffering. Read/write
inactivity deadlines allow large transfers to continue while making progress.
There is no transcoding. The existing iOS resource loader still requests bounded
ranges and handles seeking. All routes require authentication, including previews
and cached preview hits. Private service directories and symbolic links are not
exposed. There is no recursive search, continuous polling or persistent filename
index.

## Thumbnails and protection

Samba uses the same thumbnail behavior as other sources. The temporary
source suppression has been removed; legacy `disabledThumbnailSources` settings
are ignored. This takes effect when the updated API is deployed. No app update
is required.

All folders, including `/pics`, use adjacent hidden caches. The normalizer skips
`.filebeans-thumbnails`, `.filebeans-stage`, and `.filebeans-vaults` components
case-insensitively during inventory and rejects such queued media jobs. Existing
originals, completed outputs, and excluded job workspaces are retained. Private
`State/filesystem-thumbnails` caches from the earlier trial remain untouched and
are no longer used; previews regenerate next to their originals on demand.

Thumbnails are generated on request into `.filebeans-thumbnails` inside the
original file's containing folder. Cache keys cover its name, revision, fixed
320-pixel output size and renderer version. Originals are never modified.
JPEG/PNG/GIF and bounded text previews render in-process; video, PDF and other
supported image formats use FFmpeg/Poppler. Unsupported or read-only locations
retain file icons. Text previews show a bounded excerpt with ASCII font fallback.
An archive is a file: listing it does not unpack it or generate previews for its
contents. There is no archive-wide thumbnail scan.

Two renderers can run concurrently. Matching requests share a serialized cache
lookup. Files publish atomically. Preview misses opportunistically remove cached
JPEGs older than 30 days and renderer leftovers older than 48 hours in that
folder. Interrupted requests clean up their upload temporary file; spools left
by a process crash can be removed while the service is stopped. They are never
visible through the API.

Protected media, thumbnail caches and upload staging share the encrypted mount.
Renderer input uses pinned descriptors; subprocess working/scratch directories
stay inside that mount. There is no plaintext server thumbnail fallback. Broker
leases cover filesystem operations and complete response delivery, including
cache hits. Locking removes access; encrypted location hiding may return 404
rather than 409. Missing broker registry denies access, including cached reads.
Caches are excluded from browsing, copy jobs, move manifests and normalization.
Same-filesystem renames may naturally carry a folder's cache along with it.

The broker must explicitly select `"filesystemConsumer": true` during trial
cutover. Only its control UID (the API service) may then send readiness heartbeats;
the former Quantum reader cannot keep this replacement configuration ready.
The API advertises the existing guard capabilities only after startup checks.
The normalizer never registers as a consumer. Broker keys and mount privileges
remain in the separate broker process.

The app hides protected thumbnails while locked. No special encryption or secure
erasure of mobile thumbnail caches is added.

## Verification

Run project tools inside the VM development container. From this worktree:

```sh
dev bash -lc 'cd /home/dev/projects/fileBeans/.worktrees/filesystem-api/companion && /tmp/filebeans-go/go/bin/go test -race ./... && /tmp/filebeans-go/go/bin/go vet ./...'
dev bash -lc 'cd /home/dev/projects/fileBeans/.worktrees/filesystem-api && /home/dev/projects/fileBeans/.venv/bin/python scripts/validate-source.py'
sh scripts/test-filesystem-fuse.sh
```

The FUSE runner compiles test executables and uses the existing development
runtime with synthetic keys and a loopback-only CIFS fixture. It does not build
images or access production files. It covers encrypted cache persistence,
locking during response delivery, registry loss, boundary moves, local/CIFS
folder lifecycle and independent ciphertext recovery. Ordinary API tests cover
ranges, collisions, stale/concurrent writes, interrupted uploads, hidden paths,
FIFO rejection, cache invalidation and real image/video/PDF/text rendering.

Go tests requiring SQL still need `FILEBEANS_TEST_CONFIG` pointing to a dedicated
VM `.test-config` database; they skip without it. Apple source validation is
syntax/configuration checking, not Apple SDK compilation or XCTest execution.
Simulator tests, device playback/Files acceptance, runtime image validation and
live cutover remain separate steps requiring their applicable build/deployment
requests. Do not merge or change branch protections to enable a trial build.

## Requested build, cutover and rollback

Do not execute these steps without the corresponding explicit request.

1. Save current image digests, private configs, proxy routes and the installed
   app build number. Preserve Quantum's private database and original images.
   Configure Quantum to exclude `.filebeans-thumbnails` for clean rollback.
2. After committing the trial revision, prepare independent runtime contexts with
   `scripts/prepare-vault-runtime.py --filesystem` inside development. This
   compiles Linux binaries and prepares the filesystem Dockerfile; it does not
   prepare or fetch Quantum. Build requested images only with the VM image-builder
   procedure. The runtime includes FFmpeg, Poppler and pinned rclone. Deploy the
   main-branch normalizer cache exclusions before enabling adjacent thumbnails
   in its watched folder. Normalizer deployment requires a separate request.
3. Request Apple build/testing from this feature branch, preserving app/domain
   identifiers and versioning. The native player needs real startup, seek,
   resume and cancellation checks on local, Samba and protected media. Exercise
   Apple Files create/edit/rename/move/copy/delete and conflict retention.
4. Stop Quantum before enabling the trial API against the same production roots.
   Switch the broker's consumer mode and deploy the API artifact, preserving
   the normalizer container, configuration, mounted paths and running job,
   and retain existing HTTPS companion routing. Never run both backends as
   active writers. No SQL migration is needed for this replacement.
5. Existing app connections reuse companion URL/token, IDs and Files domains.
   Original Quantum URLs and primary tokens remain stored for rollback. Direct
   API connections use a single URL/token. Changed URLs require an explicit
   token rather than silently forwarding credentials to a new endpoint.
6. For rollback, stop the trial API, preserve the running normalizer, restore the previous broker
   executable/config (or set consumer mode false), restart the original
   companion/Quantum images and restore the previous native app build.
   Existing data and encrypted folders remain in their existing formats. Software
   rollback does not undo file edits made during the trial. No cache deletion,
   database restore or ciphertext conversion is required.
7. Only after owner acceptance and device/live validation, merge the feature
   branch and retire Quantum's service and build dependencies. Preserve rollback
   artifacts according to the existing release procedure.

The owner subsequently authorized the trial deployment and TestFlight release;
see the release record below and `RELEASING.md`. Main remains unchanged.

## Authorized trial release — November 18, 2026

The owner authorized deployment and app release after the temporary cache
exception was tested. Source `8af2393`, tagged `testflight/0.1.0-23`, is deployed
as the independent API runtime and released as **0.1.0 (23)**, **VALID /
IN_BETA_TESTING**. Apple validation passed **104 simulator tests** and device
archiving; the runtime image passed its isolated API/renderer checks.

Live VPS/Samba and authenticated HTTPS checks passed, including ordinary file
operations, ranges, conflicts, thumbnails and managed Trash. Quantum is stopped
with rollback artifacts preserved. The `/pics` cache exception is enabled for
source `VPS`. The normalizer was neither updated nor restarted: its original
container, start time, restart count, worker PID and active encoder PID remained
unchanged, its 743 job IDs were retained, and progress continued. A trial systemd
override prevents broker restart dependencies from stopping that worker.

See `RELEASING.md` for hashes, Apple workflow links, backups and rollback details.
Main remains unchanged until owner acceptance. Physical-device playback, Files
interaction and real-key unlock still require owner acceptance on their devices.

### Server-only copy correction and Samba preview suspension

Runtime source `eda5913` fixes single-file copies by applying thumbnail exclusions
only to directory traversal. Explicit root and nested cache exclusions keep folder
copies free of preview caches, including empty cache directories. Rclone critical
errors now reach the existing redacted error handler. Real-tool regressions run
without SQL, using the same packaged rclone and inherited-descriptor paths.

The owner requested the API stopped during diagnosis, then authorized deployment
and restart with Samba thumbnail generation disabled. The updated runtime is
serving with `disabledThumbnailSources: ["Samba"]`. Live VPS-only single-file and
folder-copy jobs passed; the reported failed copy completed on retry with original
metadata unchanged and matching source/destination contents. Verification made no
Samba file writes. The transcoder container and broker PID stayed unchanged.
TestFlight build 23 remains compatible; no app build or release was needed.

### Protected-process renderer launch fix

Runtime `e58aa16` resolves the renderer's own inherited directory descriptor as
its working directory. Access through the protected parent's `/proc/<pid>/fd`
failed under the API's process privacy policy and prevented external renderers
from starting. The corrected implementation preserves that policy, scratch
placement and operation leases. A regression runs actual video/PDF rendering in
a non-dumpable helper process and checks the policy remains enabled afterward.

The exact runtime image and encrypted local/CIFS tests passed. Live VPS video
previews and cached reads now succeed; originals, the transcoder and broker are
unchanged. Samba thumbnails remain disabled, and `/pics` caches remain outside
the watched directory. TestFlight 23 needs no update; reopening a folder starts
fresh thumbnail requests. See `RELEASING.md` for artifact and rollback details.


### Resumable app uploads

`resumableUploadsV1` advertises file-backed resumable sessions. Existing content
POST/PUT routes remain compatible. All session requests use the existing bearer
authentication and the same `source` and destination-file `path` query parameters.
The client chooses a UUID session ID and persists it before creating the session.

| Method | Route | Behavior |
| --- | --- | --- |
| POST | `/v1/fs/uploads/{id}` | Create/reconcile with JSON `{"size":123}`; repeated identical requests retain the offset. |
| GET | `/v1/fs/uploads/{id}` | Return `id`, `path`, `size`, `received`, and `completed`. |
| PUT | `/v1/fs/uploads/{id}` | Append up to 8 MiB using `Content-Range: bytes start-end/total`. |
| POST | `/v1/fs/uploads/{id}/complete` | Publish a complete payload without replacing an existing destination. |
| DELETE | `/v1/fs/uploads/{id}` | Remove session staging; a published destination is preserved. |

The actual durable payload length is authoritative after restart. Incorrect offsets
return conflict; incomplete or oversized chunks roll back to their previous offset.
After a lost response, query/reconcile before sending more bytes. Completion keeps a
staging hardlink until acknowledgement, so repeating completion can distinguish its
own published inode from a collision. Publication therefore requires hardlink support
on the destination filesystem. No completion success is reported when publication
fails. Session payloads and metadata live under the destination parent's hidden
thumbnail directory, including inside encrypted mounts. Locking prevents session
access; unlocking allows offset recovery. Cancellation removes staging explicitly.

The iOS queue persists payloads and manifests in the shared App Group. It uses up to
two file-backed background chunk uploads, reattaches outstanding URLSession tasks
before scheduling replacements, and reconciles server offsets on foreground entry.
Force-quitting may stop iOS background work; opening the app recovers pending files.
Protected destinations pause while locked, without passing encryption keys to either
extension. Google Drive retains its resumable ingress and independent server-copy
phase; previous Drive queue manifests migrate with their original idempotency keys.

### Native playback and large folders

Native playback uses a dedicated authenticated media session, separate from listing
and thumbnail connections. Each new player range starts with at most 64 KiB before
using throughput-sized chunks. Cancellation is checked before handing bytes to the
player. Existing byte-range validation and bounded buffering remain in place.
A VPS comparison with the previous file view route and real-device repeated seeking
remain acceptance checks; no live playback improvement has been claimed from static
validation alone.

The build-27 report supplied on 2026-11-20 records a background process-exit watchdog
termination (`0x8BADF00D`, five-second exit allowance). Its main thread was creating
an ISO8601 formatter during SwiftUI evaluation. App frames were not symbolicated,
so this is evidence for investigation rather than proof of the complete call chain.
Folder sorting now runs outside view evaluation, parses each date at most once per
sort input, and shares bounded timestamp caches/formatters. Protection mapping reads
the catalog once per listing, instead of once per file. Unchanged listings do not
republish, background folders do not poll, and thumbnail work is limited to four
fetches with unused requests cancelled and images downsampled before display.

### Validation and pending delivery (2026-11-20)

Source/configuration checks and Apple signing-script tests run on VM-laptop without
an Apple build. Filesystem race tests and the companion suite pass except for the
unchanged `TestRcloneCriticalErrorsAreReportedAndRedacted`: the installed rclone
returns a different diagnostic for its filter fixture. Protected FUSE/SMB scenarios
require the existing opt-in integration environment and are skipped in ordinary
unit runs. Apple simulator tests have been added/updated but not run for these
changes. Real-device scrolling, Files lock/unlock, share-sheet imports, background
uploads/force-quit recovery, and VPS seek comparisons remain pending acceptance.

Deploy the prepared companion changes only on explicit request. Before a requested
signed app build, register `com.agawlbb.myfiles.Share` with the existing App Group
and shared account Keychain entitlement, then configure `SHARE_PROFILE_BASE64` using
the existing Apple setup scripts. The extension must not receive the vault Keychain
group. No Apple registrations, signing changes, builds, uploads, or VPS deployment
were performed as part of source implementation.

## Linked archives (phase three)


Media added to a Library waits five minutes after its last completed upload,
move, rename or observed modification. A single bounded encoder uses the existing
photo/video profiles and validates its temporary output before replacing the VPS
copy. The exact original remains in hidden, protected Library staging until its
checksum-verified archive copy and thumbnail are stored. An unavailable archive
does not prevent publishing the encoded VPS copy. Retries occur after one, five,
fifteen and then thirty minutes; restart recovery preserves pending originals.
Archive media is never opened to generate a thumbnail.

FileBeans file/folder moves within the Library mirror relative paths, preserving
original extensions; Trash and restore propagate to archive Trash with its normal
retention period. Pending originals prevent premature Trash purging. Empty nested
folders are mirrored too. Moving out of the Library and replacing existing linked
media in place are blocked; move the existing media to Trash before replacement.
The archive remains read-only to users. Pairing uses exact-case relative paths and
filename stems, ignoring the final extension, rather than a permanent file index.

`archiveSyncV1` advertises `GET /v1/archive-sync/status`, retaining the summary
counts `pendingOriginals`, `pendingChanges` and `problems`, and adding
`pendingWork` (all unfinished tasks) and `items`. Each visible item contains
`id`, `name`, `source`, `destination`, `phase`, and optional `error`/`next` retry
time. Phases distinguish queued transcoding/copying, active `transcoding` and
`copying`, `retrying`, `finishing`, `updating`, and `attention`. Live worker phases
are memory-only and clear on completion or restart; names come only from records
that can currently be decrypted. Locked work contributes to summary counts
without exposing filenames or internal staging/checksum fields.

The app polls while the Transfers screen is open and active, showing archive work
beside regular transfers. It does not show an archive launch/foreground popup.
Older summary-only servers remain compatible. Queue and folder-routing metadata are
encrypted by the Library; archive unavailability does not require locking it.
External filesystem discrepancies pause discovery and raise attention rather
than inferring permission to delete originals. Perform supported moves in FileBeans;
external edits are not automatically reconciled. Discovery walks names, not media,
and existing matched files are not re-encoded.


A folder's **Link Archive…** menu links it as the Library to an existing folder,
including an existing protected folder, or a protected folder created in the
archive picker. **Linked Archive…** shows both locations, opens either folder,
and offers **Unlink**. Root folders receive a link badge alongside protection.
One pair covers both complete trees, including existing and future subfolders;
subfolders need no individual registration or matching structure. Endpoints may
share a source, but must belong to this connection. Source roots, Trash, identical
endpoints, overlapping endpoints and trees already covered by another link are
not eligible. Library changes now propagate through a persistent operation journal.
The existing normalizer configuration is not enabled or reconfigured by linking.

The archive endpoint and all descendants are read-only in FileBeans. Creating,
uploading, editing, renaming, moving, deleting, restoring or purging there returns
HTTP 403 with `Make changes in VPS /pics.` (using the actual Library location).
Mutating an ancestor or submitting/retrying a job that would write into the
archive is also blocked. This policy follows protected vault IDs and covers
filesystem aliases. It does not change operating-system permissions.
Deleting the Library endpoint or an ancestor requires unlinking first; supported
Library renames update its location. Cross-source/protection-boundary linked-root
moves still require unlinking. Unlinking preserves both trees and restores archive
writes. Copies never inherit a link. Relevant active source/destination jobs
prevent linking. Unlinking is blocked until pending archive work has completed.

External moves/deletions leave an unavailable link at the recorded location;
there is no automatic reconciliation or deletion. Ordinary endpoints check
filesystem identity to avoid silently adopting a replacement directory. Protected
locations follow the vault's identity and recorded relative path. Locked link
metadata is omitted from public link lists; protected locations remain encrypted
using the existing operation-record mechanism. When link metadata is locked,
destructive link checks fail closed, and thumbnail generation is conservatively
unavailable until unlock. Source unavailability never deletes the link.

Listings include `archiveLink`, `archiveRole` (`library` or `archive`),
`archiveRoot` and `archiveLibrary` (the matched Library source/path). Archive media
retains normal preview eligibility. Archive thumbnail requests serve only existing
cache JPEGs; missing caches return 404 before media is opened or rendering starts.
The client also sends `cachedOnly=true` for archive requests. This GET has no media
body. Generation outside linked archives remains normal. Explicit opening and
playback remain available. Unlinking restores ordinary thumbnail generation.

`companion/ops/archive_thumbnails.py` compares complete relative paths and exact-case
filename stems, ignoring the last extension, media sizes and hashes. It refuses
mismatches or ambiguous stems before writing. It imports only current cached VPS
JPEGs, rekeys them for each archive original's metadata, atomically publishes and
verifies cache bytes, and never opens original media. Missing thumbnails remain
missing. Internal cache/staging paths are excluded. The one-time operation is not
an automatic synchronization worker.

All routes use the existing companion authentication and no-store policy:

| Route | Behavior |
| --- | --- |
| `GET /v1/archive-links` | Resolved link IDs and Library/Archive endpoints with `source`, `path`, and `available`; protected links are hidden while locked |
| `POST /v1/archive-links` | Body `{"library":{"source":"vps","path":"/Media"},"archive":{"source":"samba","path":"/Originals"}}`; validates both trees using filenames only and creates the link; an identical retry returns the existing pair |
| `DELETE /v1/archive-links/{id}` | Removes the link only; requires access to its unlocked metadata |

Capabilities advertise `archiveLinksV1`, `filenameStemsV1`,
`archiveReadOnlyV1` and `archiveCachedThumbnailsV1`. Links reuse the
existing encrypted `fb_operations` storage with kind `archiveLink`; no SQL schema
change or existing-data migration is needed. Server and app updates are required
to expose setup; older clients still receive server deletion/thumbnail guards.

### Filename uniqueness

FileBeans mutations now reserve a file's name without its final extension within
its parent folder, using Unicode NFC normalization and Unicode case folding.
`Photo.MOV` conflicts with `photo.mp4`, and `café.jpg` with decomposed `café.png`.
Extensionless names keep their whole name; a leading dot alone is not an extension
(`.env` keeps `.env`, while `.env.txt` has stem `.env`). Only the final extension is
removed (`trip.tar.gz` has stem `trip.tar`). Directories retain their ordinary
full-name policy; this rule applies to files.

Upload publication (including resumable uploads), copy, move, restore and generated
outputs check the policy. The dataset renderer alone has a generation-time
exception for its established `.jpg`/`.mp4` plus `.txt` caption pairs; the dataset
format stays unchanged. This does not allow ordinary mutations to add matching
stems in exported directories. Linking or copying an existing tree containing
those pairs still requires resolving its filename ambiguity. Moving an existing
directory preserves its existing contents and does not introduce sibling-name
conflicts. Destination checks and publication are serialized, with
an internal filename-free `.filebeans-stage/filename-policy.lock` sidecar also
coordinating filesystem publishers across processes. Drive publication and
mutations share a process lock and check paginated sibling names. Existing folder
contents remain untouched and readable. An intentional edit still uses the
existing revision/overwrite protection and cannot introduce a different sibling
with the same stem. Folder copies and link setup reject existing nested conflicts;
resolve them by renaming or moving files first. External filesystem or Drive
writers cannot be forced to follow this application policy.

Verification: targeted Go race tests cover database persistence, protected record
privacy/locking, vault-relative relocation, nested membership, unavailable external
moves, unlink preservation, deletion guards, Unicode conflicts and concurrent
publication. Swift transport/metadata regressions are included for the next
explicitly requested Apple test/build. Live CIFS and physical-device acceptance
remain pending; no production changes are implied by source verification.

Source verification for this phase: the full companion race suite passes with
`TestRcloneCriticalErrorsAreReportedAndRedacted` and
`TestDatabaseWorkflow/staged-hardlinks-are-replaced-independently` excluded; both
failures were reproduced against unmodified tracked source using a temporary Go
overlay. Go vet and Swift syntax parsing pass. The existing complete source
validator still stops at its pre-existing project-target-set assertion. Isolated
real-FUSE protected filesystem lifecycle tests pass on local and synthetic CIFS
storage; the synthetic SMB resumable-upload subcase retains its existing skip
because the fixture does not implement hardlinks. Test container and staging were
removed. Apple compilation, Apple test execution, physical-device and production
acceptance await separately requested build/deployment work.

### Stage-two validation and activation

On 2026-11-25 the Go race suite (including VM-only MariaDB integration) and
`go vet ./...` passed. Seven thumbnail-import tests passed; Swift syntax parsed
across 64 files. Apple SDK unit tests still require the existing macOS workflow.
Server `30523c3` is deployed and app 0.1.0 (36) is released. Apple validation
passed 134 unit and 3 UI tests plus the Release archive. See RELEASING.md for
image identity, live checks and cleanup evidence.

### Automatic VPS thumbnail backfill

The API's elected worker scans VPS sources automatically after startup and every
15 minutes, prioritizing linked Library folders. It processes one missing preview
at a time through the existing renderer slots/timeouts, and stores the result in
the adjacent hidden `.filebeans-thumbnails` directory. Current caches are reused
without opening originals. Revisions detect changed media. Locked folders wait
until a later pass; nothing is unlocked automatically. Trash, internal caches,
symlinks, Samba sources and linked Archive trees are excluded. Directory depth
and traversal work are bounded; unsupported/rendering failures retry on later
passes. The worker does not copy thumbnails to archives or synchronize media.
