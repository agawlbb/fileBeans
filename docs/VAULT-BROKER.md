# Protected-folder broker implementation

The experimental branch includes the broker, companion guards, pinned Quantum
adapter and runtime templates. Real local/CIFS tests run the consumer in a
separate slave mount namespace. The feature runtime and app build 18 are deployed/released from the unmerged
feature branch; the production configuration is recorded below.

## Process and storage layout

`companion/cmd/filebeans-vault-broker` is a separate root process. It does not
connect to SQL or load web credentials. Its configuration allowlists storage
roots, fixed gocryptfs/FUSE executables, the companion UID, a different Quantum
UID, and a shared group. Configuration, executable files and their parent
directories must be owned by root and not writable by other users.

For a newly created folder, the layout is:

| Location | Contents |
| --- | --- |
| Storage root `/.filebeans-vaults/<id>` | Standard gocryptfs ciphertext, config, diriv and recovery instructions |
| Runtime `/mounts/<id>` | Private gocryptfs mount containing `files` and `private` |
| Selected source-relative folder | Bind mount of `files`; read-only tmpfs placeholder when locked |
| Encrypted `private/cache`, `private/tmp` | Protected consumer state |
| Broker state `registry.json` | Public IDs, selected folder locations, hashed request IDs and placeholder identities |
| Runtime `status.json` | Public descriptors and mount states; no keys or child filenames |

The registry records the protected folder's public name/location, which already
appears alongside ordinary folders. Child filenames never enter this registry.
Creation accepts only source-relative locations and a single name. Directory
descriptors and `openat2` reject symlink components. Existing data is never
converted or overwritten. The placeholder's device/inode identity is verified
before mounting. Nested or overlapping vaults are rejected.

Registry writes use atomic replacement and directory synchronization. The
reservation is durable before ciphertext is generated. An interrupted request
can retry with the same idempotency key. The same key with different arguments
is rejected. The master-key fingerprint and pending-initialization fingerprints
prevent another master key from initializing a subsequent folder.

The key travels through the local socket and a gocryptfs stdin pipe. It is never
passed in argv, environment, the registry or a key file. The existing host swap
and process-dump checks apply before accepting real keys. The synthetic FUSE
test injects a memory-policy check in test code only; the daemon has no bypass
flag or environment setting.

## Socket and operation leases

The broker listens only on a Unix socket. `SO_PEERCRED` distinguishes the
companion from Quantum. Only the companion UID can create/unlock/lock folders or
receive the in-memory derived metadata key. Quantum can read public status and
register its supported integration features. The filesystem master key is never
returned through the socket.

The shared `protection` package holds a read lock on a root-owned runtime gate
for the full operation. Mount transitions acquire an exclusive lock, with manual transitions waiting up to
ten seconds outside the broker request mutex.
Active readers return busy rather than being silently disconnected by a
successful lock. Normal unmounts are checked; force and lazy unmounts are not
used. Missing mounts reject access before cache selection or ordinary-path
fallback. Mountpoint moves and recursive ancestor operations are rejected.

The companion uses guarded roots for filesystem operations, per-vault encrypted
staging and Trash, and encrypted operation bodies labeled with vault IDs. An open
second vault does not make the first vault's locked history readable. The
normalizer rejects protected inputs/output locations and skips protected paths
during scans. A companion restart can reconnect to the still-running broker;
the broker remains the owner of the mounts and derived metadata key. A broker
or host restart does not automatically unlock folders. Stale mounts fail closed.

## Quantum consumer

The pinned upstream source is `2c071d1e6eb48d4b88da2657885d345663a0a88b`
(`v2.0.2-stable`). [The adapter](../quantum/README.md) installs operation leases,
pre-cache lock checks, encrypted preview/archive/FFmpeg temporary storage,
persistent-index exclusion, filename-bearing metadata filtering, and mutation
boundary checks. Protected files are listed directly without persistent indexing.
Protected image and video entries advertise their previews to ordinary clients.

Only the fully patched consumer registers protocol 1 with all required
features, including `eventLeaseScopes`,. Registration expires 15 seconds after its last heartbeat. Quantum
receives public state and operation leases, never the master or metadata key.
It runs under a different UID from the companion, with their common primary GID.

## Runtime preparation

Templates are source only, not installation commands:

The broker and its gocryptfs children require **effective and permitted**
`CAP_SYS_ADMIN`, `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`, `CAP_SETUID`,
and `CAP_SETGID`. The unit explicitly requests these through both
`CapabilityBoundingSet` and `AmbientCapabilities`; the bounding set alone is
only a ceiling. Startup checks the actual effective/permitted sets before
opening the registry or mounting any vault and reports missing capabilities.
Keep these capabilities confined to the broker; web containers do not need them.

On local ext4 storage, gocryptfs preserves requesting-user ownership. Its mkdir
path switches thread groups, GID and UID before creating the directory. Missing
`CAP_SETUID` can therefore return `EPERM` for a UID 1000 companion even when the
directory is `10001:1000` with mode `0770`, while root writes succeed. The reported
`CapPrm/CapEff=000000000020004b` lacks that capability; the required mask is
`00000000002000cb`. Inspect the running broker and gocryptfs, not only the unit
file or `CapBnd`. Applying a unit update requires a systemd daemon reload and
broker restart during an explicitly requested deployment, followed by unlocking
and testing writes as the companion identity. Existing encrypted data does not
need reconversion or ownership rewriting. The separate CIFS `force_owner` path
does not establish that local ownership-preserving writes work.

Regression coverage includes rejecting the reported capability masks,
reproducing the credential-switch `EPERM` in an isolated subprocess, and creating
and verifying a file as UID 1000 through an actual local gocryptfs mount. Run
`TestBrokerCapabilities`, `TestBrokerSetUIDCredentialSwitch` and
`TestBrokerLocalVaultUnprivilegedWrites` in the bounded filesystem test runtime.

- `companion/deploy/vault-broker.example.json` and
  `filebeans-vault-broker.service`: host root broker with allowlisted roots and
  fixed administrator-owned executables. Keep its host mount namespace.
- `compose.protected.example.yaml`: distinct companion/Quantum UIDs, shared GID,
  no web-container mount capabilities or Docker socket, no persistent container
  logs, and matching absolute paths with `rslave` storage/runtime binds.
- `quantum-protected.example.yaml`: merge with existing authentication and
  source permissions. Group-writable file/directory creation modes are required;
  startup refuses incompatible modes or a consumer using the wrong primary GID.
- Companion and normalizer configurations set `protectedRuntime` to
  `/run/filebeans-vaults`. Companion roots match the broker names/paths. The
  normalizer sees only its local source, and excludes protected input/output.
- `protected-proxy.nginx.conf`: disable persistent buffering and filename-bearing
  logs on both existing TLS proxy routes. Preserve authentication/TLS routing.

Before installation, prepare shared host mount propagation for the configured
source roots and runtime subtree. Verify the parent mounts are shared so dynamic
broker mounts reach each container slave bind. Runtime binds allow writes to
**encrypted private caches**, while gate/status/socket permissions remain owned
and controlled by the broker. Ordinary source directories must be writable by
the shared group; existing ownership and CIFS masks need deliberate review.
The broker pins ciphertext directory descriptors and confines plaintext setup
through `os.Root`. On CIFS it supplies gocryptfs a shared ownership view without
changing the encryption format. Version 2 moves closed public markers into opaque storage and encrypts their
locations; named markers are restored only during authenticated unlock. See
[location privacy migration](PROTECTED-FOLDERS.md#location-privacy-migration) for
first-unlock conversion, historical registry snapshots and rollback constraints.

Install gocryptfs 2.6.1 and FUSE 3 on the host, including `user_allow_other`.
Disable swap/hibernation/kernel dumps before accepting real keys; runtime checks
reject enabled swap and disable process dumps. Do not change the VM host policy
as part of tests. Systemd filesystem namespace directives must not isolate broker
mounts from consumers. A broker/host restart never automatically unlocks; stale
FUSE mounts fail closed and require controlled cleanup before startup.

Build preparation and requested image builds run on VM-laptop. The Quantum
Dockerfile retains the pinned release frontend/runtime while replacing its
backend; its native MuPDF build and PDF/text rendering checks now pass.
Use prepared feature images for companion **and normalizer** as well. The VPS
only receives prepared artifacts after an explicit deployment request.

## Verification and acceptance

Companion race/MariaDB and patched Quantum race suites pass. The isolated FUSE
runtime checks socket creation/retry, wrong-key rejection, two folders with one
key, busy locks, protected history/cache separation, and independent recovery.
Local and CIFS Quantum tests cover uploads, ranged reads, image/video previews,
pre-cache lock checks, persistent index exclusion, locked-root listings and
cross-process mount propagation, distinct-UID file handoffs, and encrypted
archive/resume-token checks. Actual broker ciphertext recovers with standard
gocryptfs without the registry, database or broker.

The disposable runtime has no external network, credentials, source mount,
Docker socket or host mount propagation. Its loopback-only SMB fixture and shared
mount subtree exist only inside its own namespace. It uses synthetic keys and a
test-code-only memory check, leaving production checks intact.

Runtime artifacts/native PDF compilation and isolated image execution pass.
Apple validation now passes all 83 simulator tests and the unsigned iPhone/iPad
Release archive. Before deciding to keep/merge: complete physical-device
acceptance, then exercise
the explicitly requested staging deployment topology, proxy and full SQL-backed
transfer/Trash flows. Root/ancestor mutation restrictions and metadata feature
limits are listed in [PROTECTED-FOLDERS.md](PROTECTED-FOLDERS.md). No main merge,
production deployment or app release is performed by this implementation.

## Prepared runtime artifacts (November 18, 2026)

`python3 scripts/prepare-vault-runtime.py`, run through `dev`, prepares static
companion/broker binaries, tested gocryptfs, matching companion/normalizer image
contexts, and an SHA-256 manifest under `.build/vault-runtime/<revision>`.
It refuses to overwrite existing artifacts. The pinned rclone archive can be
cached under `.build/dependencies/rclone-v1.76.0-linux-amd64.zip`; its checksum is
validated even when downloaded through GitHub rather than the upstream mirror.
For example, run `gh release download v1.76.0 --repo rclone/rclone --pattern
rclone-v1.76.0-linux-amd64.zip --dir .build/dependencies` inside development.

The VM image builder exported these local, undeployed images:

| Component | Image tag | Image config digest |
| --- | --- | --- |
| Quantum | `filebeans-quantum:32eee70-vaults` | `sha256:1b4f947892b4f10b9d2064480ef26efb04b784614b5dcb2a01194508adf373a0` |
| Companion | `filebeans-companion:2747b84-vaults` | `sha256:0f5de6f7d7c14880806e17be91a65cbbbb9cf99f3e6fd2c1d537432f95f81be6` |
| Normalizer | `filebeans-normalizer:2747b84-vaults` | `sha256:0de8e00754c8114cf1b6e34453150ccef66a996a7a608a2066e2f3b0b9196c0e` |

Archives and checksum metadata are under ignored `.build/images`. The tags have
different preparation commits containing the same Go implementation from
`e2eab58`; differences between those preparation commits concern packaging and
build tests.
`sh scripts/test-vault-images.sh QUANTUM.tar COMPANION.tar NORMALIZER.tar`
verifies archive checksums, loads images locally and runs disposable version/help
and FFmpeg checks. All three passed as the intended unprivileged UIDs with a
read-only root, no capabilities, no network and no source/credential/data mounts.
This is artifact execution verification, not a production-topology acceptance.

## Deployed runtime — November 18, 2026

The existing VPS services now run the prepared feature images. No image builds,
source preparation, dependency builds or test suites ran on the VPS. The host
received only VM-built artifacts and runtime configuration/install helpers.

| Component | Deployed image | Manifest digest |
| --- | --- | --- |
| Quantum | `filebeans-quantum:32eee70-vaults` | `sha256:e0868b063e8da4e2ec6d04a1944f93757b055c5e13f1abb4c635322154917708` |
| Companion | `filebeans-companion:6b8147c-vaults` | `sha256:450933ff6e32afb40d98e0a78939f7bc887df24343043aaa4b0e456e79c7609f` |
| Normalizer | `filebeans-normalizer:6b8147c-vaults` | `sha256:f87bcbc3763a6421ab2d2939e4a2f1f76aeafa276888e4bfd5cb1a666caae639` |

The companion and normalizer keep UID/GID `1000:1000` and their private state.
Quantum uses reader UID `10001`, primary GID `1000`; its existing private data
was reassigned to that UID without changing accounts, permissions or source
names. A user ACL gives the reader access to existing local source contents.
CIFS keeps its existing server/protocol/encryption/credentials with file/directory
masks `0660`/`0770`. All three running processes report umask `0007`. Web
containers retain no mount capabilities, no persistent container logs and no
Docker socket.

Fixed host bind aliases preserve `VPS` at `/folder` and `Samba` at `/samba`.
Both alias mounts are shared; consumers receive them and the broker runtime via
`rslave` binds at the identical paths. The normalizer has a separate config with
only the local root, and no Samba bind. Its queued work resumed after the update.

Host units `filebeans-vault-roots.service`, `filebeans-vault-broker.service` and
`filebeans-protected-stack.service` are enabled and active. Only the two source
aliases and these fileBeans containers depend on the new root preparation;
other Docker applications keep their existing startup order. Consumer Compose
restart policy is `on-failure`; the stack unit starts them after the broker on
boot, and stops consumers before the broker during shutdown. The alias helper
rejects symlink source paths, verifies the CIFS identity and directory identities,
pins source descriptors for bind mounts and detects bind mounts through
`mountinfo` rather than the same-filesystem `ismount` heuristic.
Installed helper SHA-256:
`dd29a75695825ab555706c1fbf5692e6467ec5091ceb1de22b798805d86f8421`.

Existing Caddy TLS routing was retained. The fileBeans site discards access logs,
handles proxy failures without persistent request logging, and streams responses.
The shared proxy and other application routes were not recreated. Swap was
already disabled; no swap-policy change was needed.

Verified live: public Quantum health and frontend HTTP 200; authenticated companion
capabilities/vault listing HTTP 200 with `protectedFoldersCreate`; anonymous
companion HTTP 401; Quantum SQLite integrity; zero container restart loops; shared
mount identities/propagation; read/write handoff between reader and companion UIDs
on both local and Samba storage. A final isolated VM check also passes real
rclone transfers between the separate consumer UIDs inside local and CIFS-backed
protected folders, including the standard metadata-copy step. Only isolated,
disposable ordinary probe files
were used and removed. Normalization remained running and its conversion progress
advanced after restart. No production protected folder or recovery key was created.

Pre-deployment configurations, SQLite snapshot and source ACLs are retained under
root-only `/opt/services/filebeans/deploy/protected-9358f30/before`. The installed
broker config is `/etc/filebeans/vault-broker.json`; public registry state is
`/var/lib/filebeans-vaults`, runtime state is `/run/filebeans-vaults`. Do not roll
back the broker or remove these paths after creating folders without preserving
the ciphertext and public registry. No reboot or real-key device test was part
of this rollout.

### Folder creation blocked by an ordinary conversion — November 18

Server hotfix `6b8147ce128b08ed240d6337cac85bbdff1039de` fixes a persistent busy
response during Add Protected Folder. The normalizer had retained a shared
broker gate lease through an entire ordinary video conversion, so creating a
folder on any source could never acquire the exclusive transition gate.

Ordinary-only normalization roots now acquire a fresh snapshot/lease around
each filesystem operation. Open ordinary file descriptors can be used by the
encoder without retaining the gate. Normalization rejects every protected
boundary, including folders created after the normalization root was opened.
Atomic renames and recursive mutations retain a lease for the complete operation.
Metadata checkpoints and ordinary operations wait up to ten seconds for a short
mount transition rather than cancelling a conversion immediately. Existing
protected consumer streams retain their original whole-operation leases.
The generic busy response no longer tells a creation request to retry locking.

Companion race/MariaDB and isolated local/CIFS/FUSE suites pass. The regression
keeps a normalization root open through broker creation, unlock/lock and preview
operations, then verifies ordinary work can continue while access/publication
into the newly created protected folder is refused. Context wait tests verify
fresh snapshots and cancellation. No production key or vault was created for
verification.

Both updated consumers were rebuilt on VM-laptop, checksum-verified and deployed
with `--no-build`. Quantum, the broker and their mounts stayed running. Live
checks show conversion active, no container restart loops, creation capability
present and the exclusive gate available in 19 of 20 immediate probes during
conversion. The previous normalizer held that gate continuously. Rollback Compose
configuration is retained in root-only
`/opt/services/filebeans/deploy/vault-busy-6b8147c`. This change requires no new
mobile build; TestFlight 18 remains current.

### Source availability and reboot recovery — November 24, 2026

Storage checks protect a specific operation or source, rather than making an
unavailable Samba share a prerequisite for the whole service. CIFS device numbers
are temporary Linux mount identities. On startup, the broker reconciles a changed
device number against the configured CIFS share and all parked marker inodes;
it does not change marker inodes or encrypted locations. A mismatching marker
leaves only that source unavailable. Returning sources are retried under the
existing filesystem gate, without restarting healthy consumers. Unlock failures
roll back that source rather than closing unrelated vaults.

The protection snapshot names unavailable sources so filesystem consumers reject
operations on them while continuing to serve healthy sources. Ordinary encrypted
file confidentiality comes from encryption, not these availability checks.

Samba now lives below a stable shared bind parent: `/samba/share` is the configured
source, and the consumer binds `/samba` with slave propagation. The storage-parent
unit does not require the remote mount. A small, bounded systemd refresh service
retries the existing CIFS mount and attaches its alias independently; the broker
then recovers that source. The local `/folder` alias is unchanged. The new helper,
units and timer are in `companion/deploy/filebeans-vault-roots*` and
`companion/deploy/filebeans-samba-refresh*`.

VM validation passed Go race tests and vet, source failure/recovery regressions,
real synthetic CIFS device reconciliation, changed-marker rejection, late-mount
propagation and protected local/CIFS lifecycle checks. The synthetic SMB server
cannot hardlink: only its resumable-upload completion subtest skips after checking
that exact capability; the local encrypted resumable-upload test passes.
The requested deployment is finished and verified. Source `52a6671` is installed
with API image `filebeans-api:source-isolation-20261124`, manifest
`sha256:49ea35e501f64b0861fcf6431f43012d15817f0fafdf2980951502651e1a580b`.
The installed broker SHA-256 is
`1b0455eb74fdd509ad2a34e87e875c15677de75a478be41878707b1d0aa97357`.
Authenticated public HTTPS capabilities returned 200; anonymous access returned
401. During a controlled removal of only the FileBeans Samba alias, Samba
returned 503 while VPS listings and API capabilities returned 200. The deployed
timer independently reattached Samba in 3.3 seconds without restarting the API
or broker; API restart count remained zero. The new parent has its own shared
mount peer group, so attachment does not duplicate mounts on the host root.
All twelve unrelated containers retained their identities and start times,
including mail. No full-host reboot was performed.

Drive's endpoint returned 403 / `Reconnect Google Drive` both before and during
the controlled outage. Its Google authorization needs a separate owner reconnect;
this is not a Samba dependency, and no OAuth credentials were changed by the
recovery configuration.

Temporary VPS `/opt/services/filebeans/.source-isolation-20261124` (including its
operation-only rollback copies) is absent. The superseded API image was removed
after confirming no container or effective Compose configuration references it.
VM `.build/source-isolation`, its bounded `.ext4` filesystem, test bundle symlink,
binaries, image export, caches and task helpers were removed; `.build` is empty.
Only current runtime configuration, executables, recovery units and the live API
image remain. No Apple app build or release was needed.


### Protected-folder creation and temporary readers — November 24, 2026

The creation route used a single nonblocking exclusive-gate attempt, unlike
manual lock/unlock. An overlapping folder listing, thumbnail or file read could
therefore reject Add Protected Folder immediately with HTTP 409 / `Protected
folder is busy`. A read-only live gate snapshot during investigation showed a
shared lease held by a `filebeans` process; no production key, vault or mount
was changed during this investigation.

Creation now uses the existing cancellable ten-second gate wait before taking
the broker request mutex. Readiness/status requests remain available while a
reader finishes, then creation rechecks the key and consumer readiness and uses
the existing reservation/mount path. Active long-running readers can still time
out with busy; the change does not force unmounts or bypass protection checks.

Two new HTTP-handler regressions reproduced the original immediate 409 and pass
after the fix: temporary-reader contention with concurrent readiness requests,
and cancellation without creating a reservation. Related broker/protection race
tests and service vet pass in a development-image test container limited to two
CPUs, 4 GiB memory/no swap, 256 processes and a disk-backed 2 GiB scratch filesystem.
Its container, filesystem, cache and control script were removed. The fixture
uses synthetic keys and stops at source validation before creating mounts.
Actual encrypted CIFS folder creation was not exercised in this task.

Deployment of the updated broker binary remains pending an explicit deployment
request. The API/app protocol is unchanged and this fix needs no Apple build.

### Thumbnail cache activity and overnight locking — November 26, 2026

Protected-folder previews now live in `.filebeans-thumbnails` directories under
the public mount. The idle watcher recursively watched those directories, so
automatic cache reads every fifteen minutes renewed the one-hour session timer
even without user activity. The watcher now skips reserved cache/staging trees
and ignores their named events on parent directories. Public file access still
renews the timer, and active filesystem operations retain their protection leases.

The twelve-hour cache-polling regression fails against the original watcher and
passes with the correction. Focused race tests and service Go vet pass. Synthetic
FUSE checks cover cache polling, global idle expiry, dataset activity, busy-lease
retry and recovery of the original bytes after unlocking.

The corrected broker still requires an explicitly requested build/deployment.
No app build is needed. Production was inspected read-only; no service, folder,
mount or key was changed. The bounded VM test filesystem and its toolchain,
caches and executables were removed after verification.
