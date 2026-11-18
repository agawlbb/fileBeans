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
for the full operation. Mount transitions require an exclusive nonblocking lock.
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

Only the fully patched consumer registers protocol 1 with all six required
features. Registration expires 15 seconds after its last heartbeat. Quantum
receives public state and operation leases, never the master or metadata key.
It runs under a different UID from the companion, with their common primary GID.

## Runtime preparation

Templates are source only, not installation commands:

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
changing the encryption format. Read-only locked stubs prevent writes through
CIFS fallback placeholders even with fixed remote permission masks.

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
| Companion | `filebeans-companion:74893ba-vaults` | `sha256:2e5dd2b4268c3930a536785a6c88d5846d8b96ff83f91e78b5b656f3b877964c` |
| Normalizer | `filebeans-normalizer:74893ba-vaults` | `sha256:4fab1831512e5af4bfae6bbaba03e23575a094dc146e17a779811ab23061fcf2` |

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
