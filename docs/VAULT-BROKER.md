# Protected-folder broker implementation

This is source on the experimental branch, not an installed production runtime.
The Quantum adapter and packaging remain unfinished. No production vaults have
been created, no deployment has occurred, and the app creation capability remains
disabled without a compatible consumer.

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
| Selected source-relative folder | Bind mount of `files`; empty, root-owned and non-writable to consumers when locked |
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

## Quantum work remaining

The pinned upstream source is `2c071d1e6eb48d4b88da2657885d345663a0a88b`
(`v2.0.2-stable`). Its adapter must install request leases, path checks, protected
preview/temp storage, persistent-index exclusion or encrypted index storage,
filename-bearing metadata isolation, and mutation boundary checks. Lock checks
must precede every cached response. Background consumers must follow the same
boundaries and drain before unmounting.

Only then may it register protocol 1 with all six required consumer features.
Registration expires after 15 seconds without a heartbeat. Merely supporting
the protocol or opening the socket does not enable creation. No adapter has
been registered on the VPS.

## Runtime preparation remaining

The example JSON and systemd unit are templates, not installation commands.
Companion and Quantum must run as distinct unprivileged UIDs with the shared
group. Storage and runtime binds must propagate host mounts into their
namespaces with `rslave`. The runtime bind must allow encrypted private-cache
writes; its root-owned gate, snapshot and socket permissions control access.
The broker intentionally uses the host mount namespace. Systemd filesystem
namespace directives must not isolate its mounts from consumers.

Before deployment, verify this topology with local and Samba ciphertext,
consumer restarts, open streams, previews, uploads, transfers, Trash, and mount
failures. Package the required tools on VM-laptop. Do not give web containers
mount capabilities or a Docker socket. Full registry-aware protected-root
rename/move/copy and Apple/device acceptance also remain outstanding.

## Verification

The Go race suite passes against the isolated VM MariaDB configuration with the
known `TestDatabaseWorkflow/staged-hardlinks-are-replaced-independently` baseline
failure excluded. The isolated FUSE runtime passes creation/retry through the
socket API, wrong-key rejection before reservation, two folders using one key,
busy-file locking, private-cache persistence across lock/unlock, ciphertext-only
storage inspection, companion guard/history checks, and standalone recovery.

The synthetic runtime has no network, credentials, source mount or host mount
propagation. Its additional ownership and UID/GID capabilities are needed for
gocryptfs `allow_other` and the broker's ownership operations. Production memory
policy, Samba and consumer mount propagation were not changed or validated by
that test.
