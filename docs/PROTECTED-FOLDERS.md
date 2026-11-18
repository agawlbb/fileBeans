# Experimental protected folders

This feature branch adds gocryptfs vault controls to the companion and iOS app.
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
to initialize a folder. The Copy to Clipboard button uses a local-only clipboard
item without an expiration. App switching retains the displayed code and setup
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

## Creating and using folders

With the compatible broker and patched Quantum running, choose **+ → Add
Protected Folder** in the current directory. Generate or import the single key,
copy its recovery code, acknowledge the backup, and name the folder. Creation
leaves the folder unlocked and returns to its parent. Protected folders appear
alongside ordinary folders with a lock badge. Selecting a locked folder presents
its actions; biometric unlock and opening the folder are separate actions.
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

Folders remain unlocked indefinitely, including when the phone disconnects.
Because all vaults share a master key, protection against server root requires
all vaults locked. While any vault is open, root could capture the shared key
and decrypt another vault even if that other mount is currently locked.
Individual lock/unlock and a mobile Unlock All action are available. Lock cancels
the serial copy worker, then requires all in-process filesystem activity to drain.
If busy, the response says to retry; no lazy/forced unmount is used. A successful
lock requires drained operation leases, verified public/private unmounts, and
an exited gocryptfs child. Failure remains visible and never becomes a claimed successful
lock. When the last vault closes, the active metadata key buffer is cleared.

Protected roots fail closed if their expected FUSE mount disappears. The public
folder is replaced by a read-only locked placeholder, including on CIFS mounts
with fixed permission masks, preventing fallback plaintext writes. Trash and
publication staging remain within `files`; rclone configuration/temp/cache files
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
| `POST /v1/vaults/{id}/unlock` | Body `{"key":"FBK1:..."}`; verifies the encrypted identity marker before starting clients |
| `POST /v1/vaults/{id}/lock` | No key required; stops clients and unmounts |

Mutation responses return the updated vault list. Busy/locked operations return
409; mount/setup failures return 503. Use HTTPS with request-body logging and
disk buffering disabled on the reverse proxy. Unlock All calls unlock for each
initialized locked vault; it is deliberately not an atomic multi-vault operation.

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
and signed-installation acceptance remain. Production deployment and a signed
TestFlight build/release have not occurred for this revision. The VM host still
has swap configured; real-key startup correctly refuses it. Synthetic test-only
memory checks do not expose a daemon bypass.

Protected-folder roots cannot be renamed, moved, copied recursively or sent to
Trash, nor can an ordinary ancestor be mutated recursively across a vault.
Operations on their contents remain available. Protected-child pins, deep user
scopes, persistent access rules and public child shares are refused to avoid
ordinary database filename leakage; permissions on the public root still apply.
Protected content cannot use OnlyOffice or the normalizer. Existing plaintext
sources/backups/history are not retroactively erased. These are experimental
branch limits to assess before deciding whether to keep or merge it.
