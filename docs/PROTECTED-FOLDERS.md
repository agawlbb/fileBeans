## VPS/Pi synchronized session

The explicitly configured VPS/Pi group uses one Lock/Unlock command and one
hour of inactivity across protected file activity on either server. The broker
retains enforcement when the client closes or the API is unavailable. A ten-second
coordination allowance accommodates the existing five-second watcher/poll cadence.
Directory polling, peer status/heartbeats and private cache activity do not reset
the deadline; imported activity is never re-exported as local activity.

Both APIs configure `protectionSync` with matching `group`, their local `name`,
the existing `peer` ID, `peerName`, and a separate shared `key`. Addresses/TLS
pins stay in existing peer configuration. Both brokers configure the same group
and their own `server` and `peer` IDs, without any network credential. Settings
shows one Protected folders control and per-member status when mixed. Other
server groups and file browsing remain independent.

Commands work through either server. Local access remains available when the
peer is unreachable. An offline/restarted member stays locked until another
explicit Unlock; no recovery key or unlock retry is stored. Lock generations,
acknowledgments and bounded command IDs are durably stored in the broker's private
`protection-session.json`; pending locks reconcile on reconnect, and a newer lock
rejects stale unlocks. Busy mounts retain pending lock intent for broker retry.
The API stores bounded, key-free command receipts in `protection-receipts.json`
under its private state directory. These pin transport retries to the original
member boots and lock generations, so a lost reply cannot turn an old Unlock
into a fresh one after Lock or restart. Receipts are never executable work or
a source of unlock keys; no background unlock retry exists.

Public `/v1/protection` and lock/unlock routes preserve local `state` and add
`group`, `groupState`, and `members` (server ID, name, state, error). Legacy vault
commands coordinate the group while retaining their list response. The private
peer route requires the separate group credential and configured member/group;
normal API and archive credentials do not authorize it. Internal snapshots carry
local activity sequence/age and effective session age, without paths or keys.
API and broker builds must both be updated before enabling the group configuration.

# Experimental protected folders

fileBeans uses gocryptfs vault controls in the companion and iOS app.
Creation is enabled only with the compatible broker and patched Quantum consumer. Nothing here installs
services, converts existing data, deploys, or enables a production vault.

## Key and recovery

One randomly generated 256-bit master key encrypts all configured vaults. The app
stores it in an app-only, non-synchronizing Keychain item using
`WhenPasscodeSetThisDeviceOnly` and `biometryCurrentSet`. Face ID/Touch ID is
required to use, export, or remove the local copy. Changing enrolled biometrics
can invalidate access; reimport the saved recovery code if necessary. The Files
extension does not receive the key. Mobile filename/thumbnail caches are unchanged.

The copyable recovery code is `FBK1:<64 lowercase hexadecimal characters>:<8 hex
checksum characters>`. The middle field is the **actual gocryptfs master key**.
The checksum is the first four bytes of SHA-256 of `FBK1:<hex key>`; it detects
copy errors, not tampering. There is no recovery password. Keep the code securely
on a device of your choice. Import that exact code on each new mobile device.
Generated keys are saved to biometric Keychain before display. A pending-backup
flag survives process termination; the same key can be displayed again after
biometric authentication. Backup acknowledgment is required before using the key
to initialize a folder. Copy to Clipboard supports Universal Clipboard for transfer to another Apple
device. Import accepts whitespace, invisible paste separators, and hexadecimal
letter-case changes, then verifies the original key checksum and stores the
canonical recovery code. Truncated codes and extra text remain invalid. App switching retains the displayed code and setup
state; an opaque overlay covers the code while the app is inactive. Hide clears
the displayed copy explicitly. Each unlock still authenticates through Keychain,
including when a code is already displayed.

Keep the entire ciphertext directory, including hidden files, every
`gocryptfs.diriv`, long-name `.name` files, and `gocryptfs.conf`. A `RECOVERY.txt`
is added to the vault with the following independent recovery instructions:

```sh
mkdir /path/to/empty-mount
gocryptfs -masterkey=stdin /path/to/ciphertext /path/to/empty-mount
# Enter ONLY the 64-character middle field, followed by Enter.
# Original content is under /path/to/empty-mount/files.
fusermount3 -u /path/to/empty-mount
```

No fileBeans account, database, original phone, or original VPS is needed. Use
standard gocryptfs forward-mode AES-GCM/HKDF settings. Never put a real key in a
command argument, shell history, environment variable, or password file. The
standard config contains a wrapped copy of the key protected by a random,
discarded configuration password; that password is not used for recovery.

## Automatic locking

The server mount broker locks all protected folders together after one hour
without public file access or changes in any protected folder. Reads, file opens,
writes, and filesystem mutations restart the shared inactivity period. Status requests, directory listings, and
private cache writes do not. The broker checks every five seconds. Active
operation leases or busy mounts defer locking; the next check retries without
cancelling transfers or forcing unmounts.

Unlocking starts a fresh timer. Timers and filesystem watches exist only in
memory; a broker restart locks folders. No app update or new key is required.
The isolated FUSE tests advance the clock through the hour boundary on both
local and Samba-backed ciphertext, including file-read resets, private-cache and
directory-poll exclusions, busy-operation retries, and recovery after unlock.

## Creating and using folders

With the compatible broker and patched Quantum running, choose **+ → Add
Protected Folder** in the current directory. Generate or import the single key,
copy its recovery code, acknowledge the backup, and name the folder. Creation
leaves the folder unlocked and returns to its parent. Protected folders appear
alongside ordinary folders with a lock badge while unlocked. Locked folders are
hidden from the browser. Unlock them from the connection's Protected folders screen.
The connection’s Protected folders screen controls lock/unlock across folders.

Creation is gated by `protectedFoldersCreate`. The broker enables this only
while the fully integrated Quantum consumer maintains its capability heartbeat.
A missing consumer disables creation rather than creating an ordinary folder.
Use the existing connection; no per-folder server configuration or separate
Quantum connection is required. An interrupted create retries its original
request ID. Existing data is never converted or overwritten.

Runtime templates, process identities and mount requirements are documented in
[VAULT-BROKER.md](VAULT-BROKER.md), and the pinned consumer adapter in
[../quantum/README.md](../quantum/README.md). The older `vaults.example.json`,
`vault-quantum.example.yaml` and per-vault client units describe the legacy
static-vault path only; they are not the current Add Protected Folder setup.

## Locking and metadata

Folders remain unlocked when the phone disconnects, until manually locked or
the server's one-hour inactivity timer expires.
Protection is one server-wide session per connection. The Protected folders
screen contains key controls and a single Lock/Unlock control, without folder
names or a folder list. Imported keys can unlock immediately; backup acknowledgment
is required only before creating a folder with a newly generated key.
Manual transitions wait up to ten seconds for filesystem leases to drain, without
holding the broker request mutex. Idle event subscriptions do not retain leases;
actual watcher reads and event delivery are checked separately. Active transfers
can still defer locking. Normal unmounts, closed private mounts and exited
children are required before reporting success; there are no forced/lazy unmounts.
The location and metadata key buffers are cleared after the session closes.

Registry version 2 stores root names, paths, previous paths and pending relocation
paths in authenticated AES-256-GCM ciphertext, using a domain-separated key derived
from the same master key. While locked, public API descriptors contain opaque
IDs/source aliases and state, without names or paths. Named public mountpoint
directories are moved to opaque markers under `.filebeans-vaults` when locked and
returned to their intended locations on unlock. Ciphertext remains in its existing
opaque storage directory. Startup recovers marker identity before serving clients.
An incomplete mount/marker cleanup remains an error rather than a successful lock.

Protected Trash
payloads live under `private/trash/<id>`, outside the browsable `files` tree.
Publication staging remains under the hidden staging directory; rclone configuration/temp/cache files
for protected transfers go under the vault's encrypted `private/tmp` directory.
Copies explicitly exported to ordinary storage intentionally create plaintext.

Sensitive companion operation bodies are encrypted with AES-256-GCM using a key
derived by HMAC-SHA-256(master key, `fileBeans metadata v1`). Each write uses a
fresh nonce; operation ID, kind and protected source labels are authenticated.
Paths, names, manifests and error details stay inside the encrypted body. Public
IDs, kinds, states, timestamps, request IDs and vault source labels remain visible.
Locked records are omitted from lists and reject direct reads. Records involving
multiple vaults require all those vaults unlocked. No schema migration is needed.
Legacy operation bodies referring to configured protected sources are encrypted
transactionally on unlock, before external clients start. Historical records
under a former ordinary source name cannot be identified automatically.

Existing plaintext database backups, binary logs, snapshots, thumbnails and
operation history are not erased by encrypting current records. Use a fresh test
database. Older companion binaries cannot decode these encrypted operation
bodies: preserve the feature binary and test database, or explicitly export the
records while unlocked before attempting a downgrade. Never test a downgrade
against production state.

To protect an existing folder, initialize a separate vault and copy into it using
the existing transfer flow. Compare the entire tree and file hashes before any
decision to remove the original. This branch does not delete plaintext originals
or rewrite existing backups/snapshots automatically.

## API

All routes use the existing companion Bearer authentication and `no-store`
responses. No key is returned in status, errors, or capabilities.

| Route | Behavior |
| --- | --- |
| `POST /v1/vaults` | Body `{"source":"...","parent":"/...","name":"...","key":"FBK1:..."}` with `Idempotency-Key`; creates an empty protected folder and unlocks it |
| `GET /v1/vaults` | Public ID/source, locked/unlocked/error state and initialization status |
| `POST /v1/vaults/{id}/initialize` | Body `{"key":"FBK1:..."}` and `X-Confirm-Permanent: yes`; initializes and unlocks a configured empty vault |
| `GET /v1/protection` | Aggregate state only: `locked`, `unlocked`, or `error` |
| `POST /v1/protection/unlock` | Body `{"key":"FBK1:..."}`; authenticates locations and unlocks all initialized folders |
| `POST /v1/protection/lock` | Locks all folders and clears session keys after verified closure |
| `POST /v1/vaults/{id}/unlock` | Compatibility route for the same global unlock operation |
| `POST /v1/vaults/{id}/lock` | Compatibility route for the same global lock operation |

Global transition responses return aggregate state only. Busy/locked operations
return 409; mount/setup failures return 503. Use HTTPS with request-body logging
and disk buffering disabled on the reverse proxy. An unlock failure attempts to
close all folders again; cleanup failures remain visible as errors.

## Location privacy migration

Deploy the matching broker, companion and patched Quantum together. The Quantum
consumer now requires `eventLeaseScopes`; an older consumer cannot enable unlock
or creation. Existing registry version 1 can start locked, but converting its
plaintext locations requires the first authenticated unlock with the existing
key. No new key is needed. Do not claim existing locations are encrypted before
that migration completes.

Before that first unlock, inventory retained registry snapshots using
`scripts/inventory-vault-location-backups.py` with explicit trusted deployment and
state directories. Configure their absolute paths in the broker's
`locationBackups` array. Only root-owned files beneath trusted parents are
accepted. On authenticated unlock these snapshots are atomically rewritten with
encrypted location fields, preserving their recovery data. A failure blocks the
unlock. External backups, filesystem history and old logs require separate
handling; this does not promise forensic erasure of historical plaintext.

Each unlocked vault also writes its root location to `private/location.json`
inside gocryptfs. Independent recovery with ciphertext and the saved key therefore
recovers both file contents and the root location without the registry or VPS.
Older broker versions cannot read registry version 2. Preserve the current broker
and encrypted state when planning a rollback; do not substitute a legacy binary
against migrated production state.

## Acceptance before keeping or merging

- Run companion tests with `-race`, isolated VM MariaDB credentials, and
  `FILEBEANS_TEST_GOCRYPTFS` pointing to the installed standalone tool.
- Run `sh scripts/test-vault-fuse.sh` on VM-laptop. It compiles through `dev`,
  then executes the synthetic broker/Quantum and ciphertext recovery tests in a disposable development runtime with `/dev/fuse` and
  `SYS_ADMIN`. The runtime has no external network, credentials, source-tree mount, Docker
  socket, or host mount propagation. SMB runs on its private loopback interface. Its root filesystem is read-only, its
  temporary files use tmpfs, and its cgroup cannot use swap. The shared
  development container keeps its existing restrictions. No image build occurs.
- In a FUSE-capable test environment, initialize two vaults with one saved code;
  exercise read/write, filenames, thumbnails, media seeking, uploads, transfers,
  Trash and restoration on local and Samba ciphertext. Verify all hashes.
- Keep a vault open across phone disconnect and access it from another client.
  Lock during transfers and streams, test busy/unmount/hook failures, and confirm
  no successful lock response while a mount/client remains usable.
- Restart/kill the companion and the host. Confirm no automatic unlock, no
  plaintext writes under empty mountpoints, and safe stale-mount recovery.
- Inspect SQL, proxy/application logs, temporary directories, cache locations,
  swap/dump settings and backups for key material and protected paths/content.
- Copy only ciphertext and the saved code to an independent Linux environment.
  Recover every file using standard gocryptfs, without fileBeans or its database.
- Build/test on the separately authorized macOS workflow and physical iOS
  devices: biometric cancellation, enrollment changes, code backup/import,
  clipboard copying, backgrounding, second-device unlock, and Files integration.

No merge, deployment, app build or release is authorized by this document.

### Source verification on November 18, 2026

The companion race suite passes with real gocryptfs initialization and isolated
VM MariaDB, excluding the established baseline failure
`TestDatabaseWorkflow/staged-hardlinks-are-replaced-independently`. That failure
was also reproduced with unchanged `origin/main` rclone code. Patched Quantum
indexing, preview, filesystem, web, user, sharing, access-rule and SQL suites pass
with race detection.

The isolated real-FUSE suite exercises broker creation/retry, two folders with
one key, busy-reader refusal, lock/unlock, encrypted caches/history and recovery.
Patched Quantum runs as an unprivileged user in a separate slave mount namespace.
Local and kernel CIFS storage tests exercise upload, ranged reads, image/video
previews, pre-cache lock checks, preview persistence, encrypted archive spools and
resume-token checks, index filename exclusion, locked-root visibility, shared
UID/GID file handoffs and mount propagation. Broker-created ciphertext is copied
and recovered using only standard gocryptfs plus the saved raw master key.
The synthetic SMB fixture uses SMB 2.0 only because of its test-server support;
production SMB negotiation is unchanged.

Swift syntax/configuration and Keychain entitlement isolation pass. Apple
[validation run 44213282822](https://github.com/agawlbb/fileBeans/actions/runs/44213282822)
passed on source revision `4ffa92f9b0cf881ea982ad81c88a1fef910788f1`: all 83
simulator tests passed with no failures, and the unsigned Release archive for
iPhone/iPad built successfully with the File Provider extension. This includes
key-session recovery/backup/authentication, catalog protection, scope boundaries,
and companion API transport tests. Local copies of the simulator results and
unsigned archive are retained under ignored `.build/apple-4ffa92f`.

Runtime image builds, native PDF/text rendering tests, and restricted image
execution pass. Physical-device biometrics, backgrounding, second-device import,
and signed-installation acceptance remain. The feature runtime is now deployed, and signed TestFlight build **0.1.0 (18)**
is **VALID / IN_BETA_TESTING**. See [the release record](RELEASING.md). The VM host still
has swap configured; real-key startup correctly refuses it. Synthetic test-only
memory checks do not expose a daemon bypass.

Unlocked protected-folder roots support Copy, Rename, Move and Delete in the
app. Ordinary filesystem mutation routes still reject mountpoint changes; the
app delegates root relocation and deletion to the mount broker. An ordinary
ancestor cannot be mutated recursively across a protected-folder boundary.
Operations on folder contents remain available. Protected-child pins, deep user
scopes, persistent access rules and public child shares are refused to avoid
ordinary database filename leakage; permissions on the public root still apply.
Protected content cannot use OnlyOffice or the normalizer. Existing plaintext
sources/backups/history are not retroactively erased. These remaining integration limits apply on main.

### Moves across protected folders

The app uses companion jobs for moves into, out of, or between protected folders.
A single-file rename inside one folder continues to use Quantum's atomic move.
Jobs accept optional `operation: "move"` and `destinationName`; existing copy
requests remain compatible. The server advertises `protectedFolderMoves`.

A move snapshots source identity, timestamps and SHA-256 contents, transfers into
destination-local staging, publishes without overwrite, verifies and syncs the
destination, then commits source removal. The original is renamed into private
source-local staging before removal; it does not enter Trash. The journal is
stored with the job's existing encrypted metadata when either endpoint is
protected. Restart recovery handles publication, quarantine and partial cleanup.
Cancellation is accepted before commit; committed cleanup resumes to completion.
Changed sources or destinations produce a conflict and preserve remaining data.
Ordinary move jobs still reject vault-root mutation and ancestors spanning
vault boundaries; protected-root operations use the separate broker path.

Protected CIFS destinations skip timestamp assignment because shared group
writers may lack ownership permission to set timestamps through FUSE. Move
verification compares content rather than destination timestamps.

The move implementation is deployed and available in TestFlight **0.1.0 (19)**.
All 87 Apple simulator tests and the device archive passed. Live HTTPS moves into
and out of the unlocked Samba vault verified content and source removal. Main's
normalizer pipeline was merged into this feature branch and deployed with its
4 GiB resource budget; protected-path guards remain in place. See the
[release record](RELEASING.md) for source revisions, workflows and rollout checks.

### Source Trash and file-operation feedback

Each source has one flat Trash view showing original filenames. Ordinary Trash
storage and legacy protected Trash directories are excluded from file listings.
Protected deleted data stays inside its originating vault's encrypted private
area and its sealed operation record is omitted while that vault is locked.
A locked protected folder is omitted from resource listings and cached browsing
views; unlocking restores its listing and existing catalog identity.

The app exposes Select and Select All for batch copy/move/delete. Cross-boundary
batch moves submit one durable job containing the selection. Destination tiles
appear dimmed with a progress indicator before submission and while the job is
active, including jobs reconstructed after app restart. Deletions are dimmed
immediately, and individual failures clear their pending state without preventing
the remaining selected deletions. The destination picker lists both folders and
existing files; files are informational rows rather than destination choices.

Legacy retained Trash entries migrate with a persisted location intent and a
no-overwrite rename between sibling directories of the same encrypted mount.
Retries reconcile a completed rename before clearing the intent. Only tracked,
empty legacy directories are removed; unrecognized contents remain untouched.
Restoration recreates shared-group parent permissions for both server users.
Rollback metadata from the deployed migration is retained inside encrypted
`private/trash-layout-before-05bcd79`. Rolling back requires reversing the Trash
location migration with that metadata; reverting only to an older image cannot
interpret the new private locations.


### Protected-folder roots and scoped Trash

The app exposes Copy, Rename, Move and Delete for unlocked protected roots.
Same-source rename/move journals and renames the empty public mount directory,
then rebinds the existing encrypted files. It retains the vault ID, ciphertext
store and private Trash. Previous root paths allow previously deleted children
to restore into the renamed folder.

Copy to ordinary local storage creates a new protected destination using the
existing master key and copies its public children through companion jobs.
Copy into an existing protected folder remains inside that encrypted boundary;
copy to Drive is an explicit ordinary export. Cross-source protected-root moves
verify the destination job before retaining the source vault in source Trash,
preserving its existing private Trash during the normal ten-day retention period.

Deleting a whole protected folder journals its public marker into the source's
hidden store and removes its public boundary. The encrypted payload and private
mount remain available for Trash while unlocked. Locking hides its Trash entry;
unlocking it from Protected folders permits Restore. Restore refuses an occupied
original path; permanent deletion closes the mount and journals ciphertext
removal. Interrupted public renames and purges resume from the broker registry.
Older brokers do not understand these lifecycle fields and must not be used to
roll back a registry after protected-root operations have occurred.

GET /v1/trash accepts an optional source query and returns retained/purging
entries without filesystem reconciliation. Recovery and expiry run in regular
maintenance. The app requests its source explicitly and shows loading or request
errors separately from an empty result. Multi-selection actions use a bottom
toolbar, and the full shaded Trash row is a navigation hit target.

### Global protection and location privacy fixes — November 18, 2026

Source changes fix idle SSE leases blocking transitions, imported-key unlock,
iPad sidebar navigation and locked root-name disclosure. The screen uses aggregate
state without enumerating roots. One global inactivity deadline locks all folders.

The companion race suite and vet pass, with the documented unrelated baseline
hardlink case excluded. The full isolated FUSE suite passes on local/CIFS storage,
including encrypted registry/restart recovery, shared inactivity, idle event
streams, folder lifecycle operations and independent gocryptfs recovery. Swift
syntax/configuration checks pass. Apple validation
[44391292854](https://github.com/agawlbb/fileBeans/actions/runs/44391292854) passed
all 103 simulator tests and the unsigned iPhone/iPad Release archive with Xcode
27.2 / SDK 27.2 on this source. The matching broker, companion and Quantum are
deployed; see the current [release record](RELEASING.md). Real-key migration
requires the user’s first authenticated unlock after deployment.


### Apple Files access while unlocked (source update, 2026-11-20)

The File Provider now includes protected roots and descendants only while their
server state is unlocked. Locked, unknown, and error states withdraw those items;
unavailable protection status fails closed. Normal permissions govern unlocked
file reads and writes. Existing source/protected-root mutation restrictions remain.
Protection transitions signal affected folder and working-set enumerators and
request best-effort eviction of materialized protected files. A completed download
is checked against fresh protection state before delivery. Neither the Files nor
the Share extension receives the encryption key or can unlock folders.

Eviction can fail for open files or unsynced edits. Locking cannot revoke copies
already delivered to Apple Files or another application. This behavior needs an
explicitly requested Apple build and real-device acceptance; it is not yet deployed.


### Shared protection delivery — 2026-11-28

VPS API `1ac7725` and Pi HA app 0.1.6 are deployed. The broker remains
`1306507` (SHA-256 `8f3d6c6ba52358fc94935040521d639cb5055159c0f7c505c3dd49b39aafbf39`).
Both authenticated public APIs report the same locked group and their own source
scopes. One live Lock command through VPS confirmed both members locked.
Owner Unlock and live shared-activity acceptance remain pending; no recovery key
was accessed. The linked archive was preserved; Pi thumbnail generation stays off.

Full Go service tests against the final runtime image, targeted synchronization
race tests and service vet passed. Apple validation 46102198409 passed 177 unique
unit tests and four UI tests; the existing HEVC seek test needed its configured
retry. Signed build 46102198390 and exact-artifact upload 46102952077 delivered
TestFlight 0.1.0 (59), source `755685b`, Apple build
`0d9db3a1-66b8-4a97-ad11-e1cb4462d0e7`, VALID / IN_BETA_TESTING.
IPA SHA-256: `f6961b0aa055f2f6450ed4d23180cf60c6c359b14ecebd9f0c406e8ed38af7db`.
The final API retry-receipt fix changes no app source. Existing tester audience
and auto-notification remain unchanged; build 58 was never uploaded.

VM test/preparation containers, disposable API images, two bounded scratch
filesystems and backing files, exported images, caches and temporary SSH key
were removed. Pi private staging and VPS `.protection-sync` staging were removed;
the temporary Pi key now fails authentication and original SSH options are
restored. Superseded VPS API images and obsolete Pi registry manifests/blobs were
removed after workload checks. All four task build/test workflow artifact lists
are empty. Current operational images remain. The older Pi access journal
`/share/.filebeans-access-62877acfd70fe72e.sqlite` remains solely for pending
migrated archive backup/publication acceptance (A-025). Existing archive retries
were preserved, not discarded. Feature branches remain unmerged; main untouched.


### Missing VPS pictures repair — 2026-11-28

Deployed VPS API/broker `e2e777a`. Native filesystem-consumer mounts now map
ownership to the API control UID (1000), while legacy browser mounts retain
reader ownership. This permits preserving timestamps without broadening API
capabilities or changing stored ciphertext ownership.

Publication prepares output timestamps before staging the visible original.
Failure restores an otherwise-hidden original with no-overwrite rename; an
interrupted publication receives one recovery attempt when it becomes visible
to the worker after startup/unlock. Ordinary backoff remains in place.

The deployed API recovered all 79 stranded originals before broker restart:
visible files rose from 670 to 749, no unpublished output retained a hidden
original, and all 79 original SHA-256 values matched visible-file readback.
The broker correction is installed and healthy/locked. Its restart left a
disconnected public FUSE bind; with API/broker stopped, both exact disconnected
aliases were detached and the underlying marker device/inode matched the sealed
registry before restart. Final state: VPS locked, Pi unlocked, group partial;
owner Unlock is needed for live timestamp/publication acceptance. No recovery
key was accessed. Existing linked archive records, queued jobs and retained
originals are preserved; Pi HA 0.1.6, thumbnail suppression and TestFlight 59
are unchanged. No app build is required for this repair.

Focused publication/shared-protection race regressions and vet pass. A real
isolated encrypted FUSE mount verified timestamp preservation as UID 1000.
The full Go service suite passes against the exact repair runtime image.
API image `filebeans-api:e2e777a-pics`, manifest
`sha256:982e064bbd103a1d96a78a13e8153dea742782d7aa956818d617397bb52bbdb6`;
transfer SHA-256 `9a8fb70d88dc5a8a8511dff3fc667025057fad567a38bf7ba332c32e8d77e1c5`.
API binary SHA-256 `7a8d169221660856ed7442815ba3eae3134399cf5871a083d74a4f3182660d8b`;
broker `b22bb207766b1febf510460ebed29ad01e7f6bbf1e7a4ab89236236bed6e61c8`.
API 2 GiB/no-swap/128-PID limits remain; restart count zero/OOM false at initial
cutover. All unrelated container identities/start times remain unchanged.

VPS `.pics-repair` staging/rollback copies and superseded API image removed.
VM preparation/test/FUSE containers, local image, two bounded scratch filesystems
and backing files, contexts, caches, test tools, exports and temporary checksum
comparisons removed. Shared builder stopped with its normal cache volume restored.
The older A-025 Pi access journal remains for pending archive backup/publication
acceptance; live archive staging is application data, not disposable task scratch.
Feature worktrees remain unmerged and main untouched.


Owner Unlock acceptance (2026-11-28): both APIs report unlocked/group unlocked.
All 79 recovered files completed publication; all 79 visible timestamps match
retained originals. Visible count remains 749; unpublished output count zero;
all 377 staged originals remain retained for archive backup. Permission errors
and attention problems cleared. The existing 298 Samba-unavailable backup retries
remain separate; 79 newly published items await backup. Missing-picture repair
is finished and verified; no further deployment or app build needed.


### Pi archive backlog repair — 2026-11-28

Finished and verified. VPS API `filebeans-api:83342fc-archive` is deployed,
manifest `sha256:e446d25012527904794c94a0bfe16d469cc3fa0ae68ed87c4163e918d1661f33`,
transfer SHA-256 `eb99dd0e00252b42b565d97660c113726c60f466b2d30766dd44390a71252d78`,
binary SHA-256 `c3b38ff3334f89babc120066225a4b95564917bbd43fa6fd4998b31d63f98657`.
Pi HA app 0.1.7 uses source `1a3a2a3`, manifest
`sha256:ffb5e0f113758388f583484dafbc2d64afe482b3afa2ab6de34672b704fa0aca`;
ARM64 API/broker SHA-256 values are
`dd53b4df472c80cb4a1d65cfe59eb17e6d2b84e392951a2280754867638c218e` and
`ddac8ed681f738fd671d69a23fc53348e8b88f787b15c248515ef8778c5f085c`.
The VPS broker remains the earlier ownership-fixed `e2e777a` binary.

Timestamp writes on Pi native protected mounts now succeed as API UID 1000.
Peer permission failures retain their actual classification; matching originals
are verified before retransmission. Alternate-extension conflicts are checked
before upload, while genuinely missing parent directories remain supported.
The full final service suite passes in the exact runtime image; focused archive,
recovery and protection race regressions and vet pass.

Of 377 retained inputs, 294 completed normal verified backup/confirmation and 83
differing versions were preserved separately on Pi. These job inputs may already
be encoded derivatives; they are not assumed to be master originals. The scoped
maintenance plan also cleared 92 unchanged, unstarted discovery intents whose
archive counterparts already existed. Both servers now report zero pending work
and zero problems after normal processing resumed; VPS visible files remain 749,
VPS retained input count is zero, and the audited Pi canonical inventory remains
749 with no removed entries. Existing canonical Pi originals were not overwritten.
Original link ciphertext remains SHA-256
`e628005f4682156549021c8de491a78bb397ebd045de6fe5d49a0ba5354107fd`.

The 83 retained user versions total 1,505,441,079 bytes in the protected Pi vault:
`/share/fileBeans/pics/.filebeans-stage/archive-recovery/<job-id>/`.
`index-20261128.json` lists exact recovery filenames, sizes and SHA-256 values;
per-job `details.json` records the canonical original and visible library path.
All copied bytes and metadata were read back before releasing VPS retention.
These are user recovery data, not task scratch; preserve until owner review or
an explicit restore/discard decision. Thumbnail generation remains suppressed
on Pi; no video/PDF tools or Apple build were added. TestFlight remains build 59.

API 2 GiB/no-swap/128-PID limits and Pi 512 MiB/no-swap/one-CPU/128-PID startup
controls remain. HA stayed healthy during transfer (Core about 0.6–1.95% CPU,
about 798–813 MiB in the observed samples). Unrelated VPS containers and the
media worker were unchanged. Both servers are unlocked with independent scopes;
anonymous protection requests still return 401.

Cleanup verified: exact VPS `.archive-reconcile` staging and drain absent;
superseded API images and old Pi registry manifests/unreferenced blobs removed;
current registry has 17 blobs / 138,906,448 bytes. VM five task containers, three
loaded API tags, bounded scratch filesystems/backings, contexts, tools, caches,
exports, audit data and temporary SSH control files removed. Shared builder is
stopped with its normal state volume restored. Temporary Pi key now fails
public-key authentication; original SSH options restored. Pi manifest staging
and the old A-025 permission journal were removed after archive acceptance.
The sole newly retained data are the user recovery versions (A-029). Feature
branches remain pushed/unmerged and shared main checkouts untouched.
