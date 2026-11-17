# Experimental protected folders

This feature branch adds gocryptfs vault controls to the companion and iOS app.
It is disabled unless an administrator configures `vaults`. Nothing here installs
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
Generated keys require acknowledgement of a separate backup before local storage.
The Copy button uses a local-only clipboard item that expires after one minute;
displayed codes are hidden when leaving the screen or backgrounding the app.

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

## Server setup for later testing

Use a disposable Linux test environment and synthetic files first. The current
scratch companion image does not include FUSE/gocryptfs or client lifecycle
utilities; it cannot run vaults as-is. The supplied example uses host systemd,
without a Docker socket. A production packaging/deployment design must be tested
separately before using real data.
The existing companion systemd unit also uses `NoNewPrivileges` and filesystem
namespacing; it must not be reused unchanged for FUSE. A suitable host unit or
mount broker must preserve the required client mount visibility and narrowly
scoped mount permissions. The supplied client units are integration examples.

1. Install gocryptfs (compatibility tested with 2.6.1) and FUSE 3. Provide
   `/dev/fuse` and permission to mount, including `user_allow_other` in
   `/etc/fuse.conf`. Companion and its isolated Quantum instance must run with
   the same Unix identity and see the same mounts. Keep the mount namespace
   stable; container bind mounts do not automatically propagate new FUSE mounts.
2. Disable host swap, hibernation and kernel memory dumps before accepting real
   keys. Startup/unlock checks `/proc/swaps`, disables process core dumps and
   process dumpability, and fails closed if these controls cannot be applied.
   Runtime code checks swap, not the complete host hibernation/kdump policy.
   Do not re-enable those facilities while vaults are in use. Managed-language
   heap copies cannot promise forensic erasure of every RAM remnant on lock;
   active key buffers are cleared and gocryptfs exits. Keys are never intentionally
   written to persistent server storage.
3. Precreate an empty ciphertext directory and an empty mountpoint. Neither may
   overlap ordinary companion roots, state, or other vaults. For Samba ciphertext,
   configure its exact mountpoint and source from `/proc/self/mountinfo` using
   `backingMount`/`backingSource`. Omit both for local ciphertext. The VPS performs
   all encryption/decryption; the Samba machine stores ciphertext only.
4. Merge `companion/deploy/vaults.example.json` into the normal private companion
   configuration. It is a fragment, not a complete configuration. A protected
   root must be exactly `<mount>/files`, with a unique source name matching its
   dedicated Quantum instance. Do not serve `<mount>/private` to file clients.
5. Use a dedicated Quantum instance per vault, keeping its database, index,
   preview cache, logs and temporary files under `<mount>/private`. The ordinary
   instance must not index the vault mount or its ciphertext. Example Quantum
   configuration and systemd unit are in `companion/deploy`. Verify settings
   against the exact Quantum version selected for the test environment.
6. Install the example start/stop hooks as fixed administrator-owned executables
   and explicitly allow only the configured vault IDs. Configure narrowly scoped
   permission to control those units; the companion must not gain unrestricted
   service-manager or mount administration. Start must wait until its clients are
   ready; stop must wait until all streams, indexers and other filesystem clients
   exit. Do not enable automatic restarts or boot startup of these Quantum units.
   Additional plaintext clients, including an optional VPS Samba export, must be
   included in the same lifecycle. Proxy access logs must omit file paths and
   request bodies, and must not buffer uploads to ordinary persistent temp files.
7. Keep the normalizer's configuration separate, without vault definitions or
   protected roots. Protected content cannot be submitted for normalization.
   A second companion process must not manage the same vaults. Startup refuses
   stale mounts; after a crash, stop clients and unmount the stale FUSE mount
   before restarting. Parent death kills the gocryptfs child.
8. In the existing connection's **Protected folders** screen, generate/import
   the master key and initialize the configured empty vault. Initialization is
   explicit and refuses existing non-vault data. A failed first mount can be
   retried with the same key. Do not rename vault IDs once initialized.
9. Add the isolated Quantum HTTPS endpoint as another fileBeans connection once
   initialized. Give it the same companion endpoint/token for transfers and
   Trash. Unlock controls remain reachable through the original connection when
   the isolated Quantum instance is stopped. Other authorized clients can use
   the isolated endpoint while unlocked. Direct access to the original Samba
   ciphertext share never provides plaintext.

The companion stores only a public SHA-256 key identifier on disk to prevent
accidentally initializing another vault with a different master key. This is not
key material. It also records public identifiers for interrupted initializations.
It does not automatically unlock after a restart or when a phone reconnects.

## Locking and metadata

Folders remain unlocked indefinitely, including when the phone disconnects.
Because all vaults share a master key, protection against server root requires
all vaults locked. While any vault is open, root could capture the shared key
and decrypt another vault even if that other mount is currently locked.
Individual lock/unlock and a mobile Unlock All action are available. Lock cancels
the serial copy worker, then requires all in-process filesystem activity to drain.
If busy, the response says to retry; no lazy/forced unmount is used. A successful
lock requires stopped external clients, a verified unmount, and an exited
gocryptfs child. Failure remains visible and never becomes a claimed successful
lock. When the last vault closes, the active metadata key buffer is cleared.

Protected roots fail closed if their expected FUSE mount disappears. The public
`files` directory exists only inside the mount, and the underlying mountpoint is
not writable, preventing jobs from falling back to plaintext storage. Trash and
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
- Run `sh scripts/test-vault-fuse.sh` on VM-laptop for the standalone
  ciphertext-copy/recovery test. It compiles through `dev`, then executes only
  that synthetic test in a disposable development runtime with `/dev/fuse` and
  `SYS_ADMIN`. The runtime has no network, credentials, source-tree mount, Docker
  socket, or host mount propagation. Its root filesystem is read-only, its
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
  clipboard expiry, backgrounding, second-device unlock, and Files integration.

No merge, deployment, app build or release is authorized by this document.

### Branch verification on November 17, 2026

Swift parsing/configuration and the new Keychain isolation checks pass. Companion
race tests pass with real gocryptfs 2.6.1 initialization/retry checks and isolated
VM MariaDB, excluding the existing
`TestDatabaseWorkflow/staged-hardlinks-are-replaced-independently` failure. That
failure also reproduces with the unchanged `origin/main` rclone implementation.
The independent FUSE recovery test now passes using the isolated runtime:
initialize, mount, write, unmount, copy ciphertext, and recover with the exported
master-key format through standalone gocryptfs. The runtime is removed after the
test. Full companion/Quantum lifecycle integration, Samba behavior and Apple
compilation/device tests remain required. FUSE 3.17.2 is installed in normal
development, but device/mount privileges are granted only to the isolated test
runtime. The VM still has swap enabled, so actual companion vault startup
correctly refuses keys under its present host policy. No host security policy
was relaxed.
