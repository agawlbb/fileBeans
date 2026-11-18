# fileBeans companion

Single Linux service for durable server-side copies, local trash, and a full-access
Google Drive source. This branch optionally enables the standalone filesystem API
for native fileBeans and Apple Files, replacing Quantum. See
[filesystem API trial and rollback](../docs/FILESYSTEM-API.md) for the current
connection, runtime, thumbnail and protection configuration. Historical Quantum
integration notes below describe the retained rollback deployment.
Drive exists only in the main iOS app, never in its Apple File Provider catalog.
No live deployment, Google consent, database provisioning, or Apple build is
performed by these files.

## Configuration and installation

1. Install Go 1.26 or newer in the development container. Run `go mod download`
   and `go test ./...` from this directory through `dev`. A release binary can be
   built from `./cmd/filebeans` when a build/deployment is requested.
2. Provision a dedicated MySQL/MariaDB database using InnoDB. Use a separate
   migration account for the explicit `filebeans -config PATH -migrate` command;
   normal startup does not create or migrate tables. `filebeans -schema` prints
   the exact embedded migration SQL without reading configuration or connecting. The runtime account needs
   SELECT, INSERT, UPDATE, DELETE only on that database. Keep the DSN private and
   set connection/read/write timeouts as in the example. The API never sends SQL
   credentials to the app.
3. Copy `deploy/config.example.json` to a private path outside Git, e.g.
   `/etc/filebeans/config.json`, mode 0640 with access restricted to the service
   user. Generate a random API token (at least 32 characters). The state directory
   must be private, outside served folders and source roots.
4. Root names must match Quantum source names exactly. Root paths must represent
   the same user-visible scope as Quantum's `/` for that source. Do not expose
   overlapping roots or map multiple Quantum scopes to the same root. The single
   companion token authorizes all configured roots; this is a trusted single-user
   service, not an implementation of Quantum's per-user authorization.
5. Use the existing Samba mount. Configure its exact mountpoint and source from
   `/proc/self/mountinfo`; the service verifies them before accessing it. Do not
   give the service mount privileges or Samba credentials. Avoid nested mounts
   inside a root: trash and staging require same-filesystem rename support.
6. Run with a Unix identity/group compatible with Quantum. Published copies are
   0660 files / 0770 folders; configure shared group ownership/inheritance (and
   Samba ownership options) so both services can read/write the intended data.
   Trash retains the original object's permissions. Verify this on real storage.
7. Adapt the supplied systemd unit to the actual roots and user/group. Install
   only after approving deployment. The service creates `Trash` in each available
   root; unavailable shares are retried. `.filebeans-stage` is private staging and
   is filtered out of fileBeans browsing. Quantum can see these as normal paths;
   configure its exclusions for staging if desired. Never edit staging contents.
8. Proxy HTTPS `/companion/` to the loopback listener, stripping `/companion`:
   `/companion/v1/jobs` reaches `/v1/jobs`. Expose `/companion/oauth/callback` too.
   Do not log Authorization headers, OAuth query strings, or request bodies.
   Keep the listener private and do not add browser CORS access.
9. Enter the HTTPS companion base URL and API token in the fileBeans connection
   editor. Existing connections without a companion retain their old behavior.
   Configured connections fail protected deletion safely when the companion is
   unavailable; there is no destructive fallback.

## Google Drive

Reuse the existing MakeLORA Google Cloud project/Web OAuth client by privately
configuring its client ID/secret here and adding the exact new HTTPS redirect URI
in Google Cloud. Do not share or copy MakeLORA's token file. Google settings are
administrator-supplied configuration; account selection happens at consent time,
not in code. Google's project publishing/test-user policies still apply.

Inside fileBeans, open **Google Drive → Connect Drive**. The companion returns a
Google consent URL with state and PKCE. The browser callback exchanges the code;
access/refresh tokens are stored in mode-0600 server state, never returned to iOS.
The service requests the full `drive` scope. Existing read-only grants keep browsing
and Drive-to-server copying until **Upgrade Drive Access** completes fresh consent.
The companion saves granted scopes and Google's opaque account identifier. A
private identity ledger retains account nonces without tokens, so explicitly
reconnecting an earlier account can resume or cancel its unfinished jobs. A
same-account upgrade preserves job identity; switching accounts gives a new
identity, so old jobs cannot silently operate on another account. MakeLORA's token
file and authorization are independent and are never edited. Disconnect removes
only this service's saved grant and pending OAuth state.

My Drive, Shared with me, and Shared drives support paginated browsing. The app
uses the same folders-first list/grid, sort, search, selection, context menus and
destination picker as VPS/Samba. Create folders, rename, move within Drive, upload
from Files/Photos, copy Drive-to-server, server-to-Drive and Drive-to-Drive, and
restore or permanently remove native Drive trash. Google-native documents and
shortcuts open in Google's browser; supported Drive copies preserve their native
format. Export/conversion to VPS/Samba remains deliberately unsupported, including
native documents found inside a copied folder.

Drive thumbnails and supported media ranges are authenticated through the
companion. Google credentials and thumbnail URLs never reach the phone. Media
uses native playback without transcoding. Server/Drive copies never relay file
bytes through the phone. Files/Photos uploads first resume into private companion
spool storage, then become durable jobs using Google's resumable upload protocol.
The app distinguishes these two phases and retains its retry payload until the
job completes or cancellation is acknowledged. Keep free disk space for pending
uploads in the private state directory.

Drive destinations use a private marked staging folder, persisted generated IDs,
resumable session URLs, and publication checkpoints. Native Google document copies
use operation markers to recover a lost response without blindly issuing a second
copy. Such copies report item progress because Google supplies no byte progress.
Destination-name checks happen before copying and again before publication.
Google Drive has no atomic unique-name operation: an external writer racing the
final check can still create a duplicate name. Existing files are never replaced.

Google scope reference: https://developers.google.com/workspace/drive/api/guides/api-specific-auth
Download/range reference: https://developers.google.com/workspace/drive/api/guides/manage-downloads

## Copy behavior and recovery

One queue worker runs jobs serially under the existing database lease. Copies
whose destination is VPS/Samba use **rclone 1.76.0**, rather than a custom per-file
copy engine. FileBeans validates the selected roots and final destination, starts
the tool, records its JSON progress about once per second, and publishes each
completed selection with a no-overwrite rename. Rclone handles folder traversal,
parallelism, transfer checksums, ordinary source-change checks, retries and
partial files. No per-file database manifest, prefix hashing or forced fsync loop
is added by FileBeans.

`rclone copy` / `copyto` run only inside a job's unpublished staging directory.
Drive files use rclone's ID-based `backend copyid` operation. Existing final
selections are never overwritten. Retry retains staging and lets rclone compare
and skip already completed files. Ordinary copy does not delete staged files that
have disappeared from the source; it is not a mirror/snapshot operation. Rclone
may restart an interrupted file, while completed files can be reused. Older
per-file manifests are converted into one persisted selection entry per item,
without removing their staging. A crash between publication and its checkpoint
is reconciled before copying again.

The existing Drive login is passed privately to the child, using the app's OAuth
client. It is not placed in command arguments or Git. An isolated mode-0700
working directory and mode-0600 config hold any rclone token cache; they are
removed after the child exits. The global rclone/recipe configuration and saved
Google connection are not replaced. Account changes and cancellation stop the
child before cleanup/publication. Error messages retain object/operation details
while redacting credentials. Process configuration defaults to `/rclone`; the
optional private `rclone` setting can choose another binary.

FileBeans keeps the existing mounted Samba share; it does not reconfigure mounts
or obtain another set of SMB credentials. The service uses umask 0007, giving
new local files/folders group access without a separate chmod traversal. Rclone
never follows local source symlinks in this integration; skipped symlink or
ambiguous-source warnings prevent publication. Native Google documents remain
unsupported for download rather than being automatically exported. Drive
uploads, Drive-destination copies, and the separate video normalizer retain their
existing code and job formats.

Install the pinned, checksum-verified static dependency **inside development**:

```bash
dev python3 scripts/prepare-rclone.py build/tools
```

Package `build/tools/rclone` and `build/tools/rclone-README.txt` alongside the
compiled server as required by `deploy/Dockerfile`. Its README retains rclone's
license notice. Unit/integration tests can select this exact executable with
`FILEBEANS_TEST_RCLONE=/home/dev/projects/fileBeans/build/tools/rclone`.

Rclone performs transfer retries using its standard retry behavior. Exhausted
copy errors are shown with details and can be retried without clearing staging.
Other service operations retain capped exponential backoff and Google's
Retry-After where supplied. Authorization problems wait for reconnection. Missing
files, permission errors, unsupported items, collisions and disk exhaustion fail
for user intervention. Cancellation removes unpublished job staging; already
published selections remain. Failed jobs retain staging until retry/cancellation.
For a partially published multi-selection job, cancel it and submit the remaining
items separately rather than replaying published selections.

The app persists uncertain submissions' idempotency keys outside the item catalog;
retrying the same selection/destination after a lost response reuses the key.
Transfers can be inspected/cancelled after app relaunch. Folder listings refresh
while visible. Apple Files continues using its own copy transfer implementation.

## Trash behavior and external changes

App and File Provider deletes move local selections to
`Trash/<entry-id>/<original-name>` on the same root. The database records the
original path and a fixed ten-day expiry measured from deletion. Folder trees
are one retained entry. Restore recreates parent folders but never overwrites.
The app confirms permanent deletion and Empty Trash; expiry cleanup runs hourly
and at startup. Missing shares retry without touching the mountpoint directory.

File Provider does not offer permanent trash deletion. Its protected trash items
cannot be renamed, deleted again, or used for writeback. Restore and permanent
removal are available in fileBeans. Direct Quantum deletions bypass this policy.

Database intent is recorded before moves/purges. Startup and trash-list reads
reconcile interrupted operations. Untracked trash files are never automatically
purged; externally removed entries become missing. A completed restore is retained
as history. Protect the database and trash together in any future backup policy;
no backup job is installed here. Retention uses UTC Unix timestamps and requires
an accurate server clock.

The main app shows a separate bottom Trash shortcut only at each source's top
level. It hides the managed local `Trash` folder from ordinary app listings and
has no combined Trash source. Quantum and Apple Files presentation is unchanged.
Drive's Trash is Google's native trash, including items deleted outside fileBeans,
with Google's normal 30-day retention. My Drive and each shared drive have separate
scopes. Emptying requires explicit permanent confirmation and persists a snapshot
so retries never broaden the original deletion set.

## API

All `/v1` endpoints require `Authorization: Bearer TOKEN`. JSON responses are
no-store. Callback state is one-use and expires after ten minutes.

| Endpoint | Purpose |
|---|---|
| GET `/v1/capabilities` | Root mappings, trash paths, Drive connection/write state and additive feature flags |
| POST `/v1/jobs` | Submit `{items:[{source,path}],destination:{source,path}}` with Idempotency-Key |
| GET `/v1/jobs`, GET `/v1/jobs/{id}` | Progress/history; manifests stay server-side |
| POST `/v1/jobs/{id}/cancel` | Cancel work and clean unpublished staging |
| POST `/v1/jobs/{id}/retry` | Retry a failed, unpublished job |
| POST `/v1/trash` | Trash `{source,path}` with Idempotency-Key |
| GET `/v1/trash` | Retained/problem entries with original paths and expiry |
| POST `/v1/trash/{id}/restore` | Restore without overwrite |
| POST `/v1/trash/{id}/purge`, POST `/v1/trash/empty` | Require X-Confirm-Permanent: yes |
| GET `/v1/drive/files?folder=root&page=...` | Paginated files; folder may be `shared` or a Google ID |
| GET `/v1/drive/files/{id}` | Metadata and Google capabilities; no thumbnail URL |
| GET `/v1/drive/files/{id}/thumbnail`, GET/HEAD `/v1/drive/files/{id}/content` | Brokered thumbnail and byte-range media/download |
| POST `/v1/drive/mutations` | Idempotent folder/rename/move/trash/restore/purge/empty; purge/empty require permanent confirmation |
| GET `/v1/drive/trash?scope=root&page=...` | Native account trash, or selected shared-drive ID |
| POST `/v1/drive/uploads`, GET/PUT `/v1/drive/uploads/{id}` | Idempotent upload creation, offset recovery, bounded Content-Range chunks |
| POST `/v1/drive/uploads/{id}/cancel` | Cancel spool or its queued/running copy job |
| GET `/v1/drive/drives?page=...` | Paginated shared-drive roots |
| POST `/v1/drive/connect`, POST `/v1/drive/disconnect` | Separate server-side Google connection |
| GET `/oauth/callback` | Public OAuth callback, protected by state and PKCE |

## Verification

Copy cancellation interrupts in-flight Drive requests. Jobs cancelled before
discovery finish immediately without waiting for the serial copy worker. Active
jobs retain their cancellation flag until staging cleanup finishes; a Samba
filesystem call already in progress must return before the worker can clean up.
The app shows cancellation progress and the submitted source/destination, with
Drive display names included by newer clients even before discovery starts.

Use `dev` for every command. Unit tests use only synthetic files and HTTP fixtures.
For MariaDB recovery tests, explicitly set `FILEBEANS_TEST_CONFIG` to a JSON file
under `/home/dev/projects/.test-config/` with host/port/user/password/database.
The test refuses an existing `fb_operations` table, creates its own, and removes
only that table afterwards. Never point tests at production.

```
dev bash -lc 'cd /home/dev/projects/fileBeans/companion && go test -race ./...'
```

Before production use, verify real Samba reconnect and rename/fsync support,
service/Quantum permissions, actual Google consent/downloads, token expiry,
server restart recovery, and deletion/restore through the installed Apple Files
extension. Linux static Swift parsing is not an Apple SDK compilation or device
test. An iOS build/release requires its separate existing workflow authorization.

## Deployed container packaging

The VPS uses the infrastructure repository's `infrastructure/apps/filebeans/compose.yaml`
instead of the standalone systemd example, so it can reach the existing private
MariaDB network without publishing a database port. Compile a static binary in
the VM development container, package it with the CA bundle using
`deploy/Dockerfile` (or import the equivalent root filesystem), and transfer the
prebuilt image to production. The VPS retains the runtime binary, CA, schema SQL,
Dockerfile and source revision for recovery. Do not build on the VPS.

## Rclone companion deployment — November 17, 2026

### Experimental encryption branch deployment

Feature branch `feature/encrypted-vaults`, source
`9f342269f2a667300685dc251481a5f307ca6c25`, is deployed as
`filebeans-companion:9f34226`. The image was built with the canonical VM-local
builder and its transferred archive SHA-256 verified before loading:
`719a207091bea63e600b81e8e11724adb115ee1247b86d18003714dd328c6c66`.
Image config: `sha256:322a4c54bf5029e7986d5fa90d32a9d712e2ce7c35c6b9b3794d2250f4cb78a9`.
Artifacts and provenance are retained under
`/opt/services/filebeans/deploy/companion-9f34226`.

Only the companion was recreated using `--no-deps --no-build --pull never`.
The normalizer retained container `21e2ce3705f1` and start time
`2026-11-16T17:10:17.690121031Z`. Authenticated HTTPS, anonymous denial,
Samba-to-VPS copy/checksum, both storages' Trash/restore/conflict/purge, and
existing Drive browsing passed. Synthetic probe files and history were removed.
No schema migration or existing-file conversion ran. The live configuration has
no vaults; encrypted storage/client runtime provisioning remains separate from
this code rollout. The feature branch remains unmerged.

### Previous rclone deployment

Source `e06447241e001258c2574cf9ab68b75805b64874` was deployed as
`filebeans-companion:e064472`, including static rclone 1.76.0 and its license
notice. The development-built package was checksum-verified before importing
and activating only the companion. Runtime and rollback artifacts are retained
under `/opt/services/filebeans/deploy/companion-e064472`. No database migration
ran, and the unchanged iOS API remains compatible with TestFlight build 16.

Authenticated health, anonymous denial, the real-rclone VPS/Samba copy checks,
trash/restore/conflict/purge and existing Drive browsing passed. The video
normalizer retained its container ID and original
`2026-11-16T17:10:17.690121031Z` start time throughout deployment and verification.

The 32-file VPS-to-Samba probe took 23.120 seconds with rclone under the observed
network conditions; all hashes matched and probe files were removed. The previous
custom-worker probe was 8.729 seconds at a different time. This is not evidence
of a performance gain from the engine replacement. Observed storage-network RTT
was approximately 54–66 ms.

Retrying the existing CrewBeans Drive-to-Samba job reused its staging and copied
another 5,506,279 bytes, but failed after approximately 562 seconds including
retries. The actionable error was failure to create
`Retirement-2026-10-23/app/web/icons`. A temporary directory probe confirmed that
`icons`, `iconx`, and `IconX` are rejected while `icon`, `icons2`, and `pictures`
are accepted. Read-only inspection of the installed Home Assistant Samba share
12.11.0 confirmed its `veto_files` includes `icon?`, which matches those rejected
names. No share configuration was changed. The job remains failed with staging
preserved and its destination unpublished; correcting the share restriction is
required before retrying. The failed attempt is not a valid throughput benchmark.

## Companion deployment — November 16, 2026

Companion source `84ecad127bcb62d2cc1ab18d27076a3f105c329d` is deployed as
`filebeans-companion:84ecad1`. It was compiled and packaged in VM development,
verified by SHA-256 on the VPS, and activated with Compose targeting only
`companion` (`--no-deps --no-build --pull never`). No database migration ran.
Runtime artifacts and rollback copies are retained under
`/opt/services/filebeans/deploy/companion-84ecad1`.

The deployed Compose configuration uses `FILEBEANS_COMPANION_REVISION` for the
companion, falling back to the existing `FILEBEANS_REVISION`. The latter remains
`7ebd703` for the unchanged normalizer. Its container ID and start time
(`2026-11-16T17:10:17.690121031Z`) were verified unchanged after deployment, and
its current conversion continued making progress. Do not recreate the normalizer
when updating only the companion.

An isolated live VPS-to-Samba probe copied 32 files of 512 bytes each through a
deep directory hierarchy. The previous server took 30.757 seconds; the new server
took 8.729 seconds (3.52x). All hashes matched and the probe files were removed.
This is one small-file Samba comparison, including discovery/publication and
polling overhead; it does not measure Google download bandwidth or isolate each
optimization. The existing authenticated HTTPS, anonymous-access rejection,
Samba-to-VPS copy, both storages' trash/restore/conflict/purge, and Drive-browsing
smoke checks also passed. Synthetic smoke-test files and their isolated history
were removed; the two performance-probe completion records remain as audit data.

## VPS media normalization

An optional dedicated worker normalizes media through a bounded pipeline. It
never mounts or reads Samba. Configure `normalization`
in private config with `enabled`, local root `source` (for example `VPS`),
`path` (`pics`), optional `driveFolder`, `minFreeBytes` (default 2 GiB), and
`stableSeconds` (default 60). The API process needs no encoder. Run a separate
FFmpeg-enabled image with `-config /config/config.json -normalize-worker`, sharing
private state, MariaDB and the VPS root only. Use one worker replica with a
**4 GiB memory limit**, no swap, uncapped CPU,
and CPU shares 128. Merge `deploy/compose.normalization-resources.yaml` into the
existing stack for an explicitly requested deployment. The global advisory lock
and filesystem fence prevent duplicate coordinators across replicas. Each job
owns a private scratch directory; preparation/download, encoding, and validation
can overlap, with at most two encoders, one preparation/download, one validator,
and four admitted jobs total. Each encoder uses a four-thread x265 pool with two
frame threads; validation uses two decoding threads. Set `normalization.sequential: true` to disable stage overlap
without changing encoding quality.

POST `/v1/normalization/jobs` accepts `{source:"drive",path:"DRIVE_ID"}` for a
file or folder; folder requests require `Idempotency-Key`. Folder inventory reads
metadata only, preserves hierarchy/empty directories, and records unsupported
items as visible failures. A file request may specify a library-relative
`relativePath`. Drive IDs/account bindings are configuration or request data,
never compiled account settings. Drive originals remain unchanged.

GET `/v1/normalization/status`, `/jobs`, `/jobs/{id}` and `/batches` under the
normalization prefix report configuration, phases, progress and batch totals.
POST `/jobs/{id}/cancel`, `/jobs/{id}/retry` and `/batches/{id}/cancel` control work.
The existing copy-job API remains unchanged. In the app use Transfers → Media
Normalization, or a Drive item's “Normalize to VPS pics” context action.

A Drive file downloads into `.filebeans-stage/normalization-jobs/JOB_ID/input`,
is checked against available SHA-256/MD5 and its pinned Drive version, encoded,
fully decoded for validation, published with no-overwrite renames, and cleaned.
Only one input can prepare ahead of the encoder. Space admission reserves the
input plus output allowance across all admitted jobs above the free-space floor.
A running encoder is cancelled if free space reaches that
floor. Disk-space waits pause all normalization downloads. Interrupted downloads
and encodes restart that one file; completed publications are fingerprinted and
never repeated. A private owner marker fences recovery and cleanup.

The worker stops admitting jobs at 3.25 GiB working memory. Above 3.5 GiB it
cancels the newest unpublished task and switches to sequential execution for the
rest of that worker session; the task retries with its source intact. Working
memory uses cgroup v2 accounting minus reclaimable inactive file cache. Missing
cgroup accounting also restricts execution to one job. Per-job logs record elapsed
time and current working memory. Interrupted inputs and encodes may restart;
completed files are never re-encoded by this upgrade.

Before rollout, let the active file finish and stop the old worker before starting
the new image. Keep the existing database, state directory, and library mounts.
The worker recovers the legacy shared workspace before admitting new jobs, and
recovers any isolated job workspaces after a restart. Do not change the quality
profile ID or recreate import batches. Observe the first three files for memory,
progress, and publication; use sequential mode if overlap reduces throughput.
The encrypted-vault feature branch combines this pipeline with protected-path
guards. Normalization remains restricted to the ordinary VPS library.

New stable files in VPS `pics` are discovered by 30-second reconciliation scans
when the pipeline is drained, avoiding races with publication.
Source fingerprints and completed output versions prevent conversion loops.
Publication records intent before moving a local input into protected staging;
failed publication restores it without overwriting a competing file. Unresolved
conflicts retain that original and pause the queue for intervention. Once
publication commits, ordinary later user edits/deletes do not resurrect the
previous output. Existing protected Trash behavior remains unchanged.

Profile `ipad-hevc18-v1` uses HEVC Main 10, medium/CRF 18, AAC stereo 192 kb/s and
MP4 fast-start, preserving frame rate and fitting proportionally within a
1080-pixel short edge/2560-pixel long edge without upscaling. Compact compatible
HEVC MP4 files are remuxed; compliant JPEG/transparent PNG and supported animation
are retained. Other still images become high-quality JPEG or PNG for alpha.
HDR sources are tone-mapped to SDR BT.709. Source URLs/playlists are not accepted
by the media demuxer. Collisions and unsupported/corrupt media are reported;
backfill scratch is removed and Drive sources retained. Failed local inputs stay
on VPS. Completed incoming originals are replaced only after output validation.

The worker must be registered with the existing VPS backup writer/inventory
policy; staging already lives under the excluded `.filebeans-stage` tree. Backup
cadence/retention and Samba library/archive folders are not changed. Confirm
backup coverage and perform a pilot playback check before starting a full import.

The API and worker serialize Google grant changes with a shared private file lock.
The worker also holds a filesystem fence while recovering a database lease loss.
Publication takes a shared `.normalization-publish.lock` through original cleanup
and the final checkpoint. The VPS backup coordinator takes that lock exclusively
before pausing writers; unresolved held originals stop capture with an explicit
error rather than producing a snapshot missing input data. After a restore that
omits scratch, a publication intent with its visible input intact is re-encoded.
Repeated interrupted pre-publication attempts stop after three recoveries with a
resource diagnostic. Background scans do not recreate a user-deleted library.

### Shared-user Trash permission repair — November 18, 2026

Companion source `0ab86d790be34f72b9e796af0fc2b1196dc43a42` is deployed as
`filebeans-companion:0ab86d7-vaults`. Trash preparation now accepts the shared
0770 directory permissions used by the separate Quantum and companion users.
It no longer fails by trying to chmod a Trash root owned by Quantum.

The regression failed before the repair and passes on real local FUSE and CIFS.
Focused API lifecycle tests and six deployed HTTPS file/folder cases across VPS,
Samba and protected Samba passed, including restore and permanent deletion.
See [the test record](../docs/TESTING.md#shared-user-deletion-regression--november-18-2026).

The prepared archive SHA-256 is
`8d04c7b14be6e51f264c16a4f1527b7c006ee995c28f6b1fe693a26a1b41441a`;
image config digest is
`sha256:9793b8098446a1813717dde07dc7278d43620de71df23190e90f0ed2d75ecfb7`.
Rollback compose and image metadata are retained under
`/opt/services/filebeans/deploy/trash-0ab86d7`.
Only the companion was recreated; Quantum, the normalizer and the key-holding
broker kept their process/container identities. No app build or schema migration
was needed; existing TestFlight build 19 uses the repaired API.

### Source Trash and locked-folder listings — November 18, 2026

The companion runs `filebeans-companion:05bcd79-vaults`; Quantum runs
`filebeans-quantum:f334c3a-vaults`. Normalizer `87ae6ef` and the host broker
were preserved. Protected Trash payloads are in encrypted `private/trash/<id>`,
outside the public folder tree. Source Trash lists original names directly and
omits records belonging to locked vaults. Old failed/restored/purged records no
longer appear as navigable Trash entries.

The migration persists location intent before its no-overwrite rename and
reconciles interrupted intent/rename states. Only tracked empty legacy
directories are cleaned up. The live retained item matched its pre-migration
fingerprint, and its public per-folder Trash directory was removed. Recovery
metadata was saved inside encrypted `private/trash-layout-before-05bcd79`.
Rollback requires reversing the layout migration before using an older server
that does not understand the new locations.

Prepared archive SHA-256 values:

- Companion: `3b9d764aa38ffc1fff763ce6385c525c20dd310fe2f9dbcfcb8e4896ab927ac9`.
- Quantum: `a6678e6c99a818375708d6dc4b0868749077ec73aab693d6a3913aa78302e483`.

Compose backups and image manifests are retained under
`/opt/services/filebeans/deploy/browser-05bcd79`. Focused race/MariaDB, shared-user
FUSE/CIFS, migration/recovery, scope/visibility and live batch/Trash checks pass.
See [the testing record](../docs/TESTING.md#six-file-management-changes--november-18-2026).
TestFlight build 20 is available for the corresponding browser controls and
operation feedback.
