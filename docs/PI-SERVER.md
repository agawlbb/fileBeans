## Server corrections deployed — November 28, 2026

Runtime source `03ab502` is deployed on both independent servers. VPS API image
`filebeans-api:03ab502-fixes` has manifest
`sha256:02cb1f178f561d4ccc0a4c5eefe125c85188f73a034333b7f89dfd76b0e6f0c3`
and transfer SHA-256 `fa9e75c06fd5ff9d4f608a290cf14b6fb501a5bf7a7072138ed973825882ded7`.
HA FileBeans app 0.1.4 has manifest
`sha256:12c1ed86fb05f91bf65e6e5749a6256781ea237be37dc7988f8b8215bab2a3d1`,
transfer SHA-256 `7828742ee7c97012f79444075fe5d8ea4e3ae101267d28c298247225a603f3d8`,
and 102,785,874 current registry blob bytes. Pi uses the prior verified runtime
layers with only its API binary replaced; no video/PDF tools were added and
thumbnail generation remains disabled. Direct cache lookup supports Samba's
100 ns timestamp revisions. Legacy archive-policy upgrades and public archive
metadata/error corrections are deployed too.

The full Go service suite and vet pass. The runtime suite needed the actual
image's FFmpeg/rclone, package working directory and a temporary real procps
helper; no test-only tools were added to either production image. Both servers
advertise only their own scope and reject anonymous requests. API memory remains
2 GiB/no swap; Pi startup confirms 512 MiB/no swap/one CPU/128 PIDs. Pi app RAM
was 17,223,680 bytes; HA Core RAM 902,356,992 bytes at 1.29% CPU. All other VPS
container identities/start times stayed unchanged. Original archive-link
ciphertext still hashes to
`e628005f4682156549021c8de491a78bb397ebd045de6fe5d49a0ba5354107fd`.
The pre-existing 297 backup tasks remain journaled/retrying with unavailable
storage; they were not discarded or treated as permission to alter originals.
Pi protected folders relocked on app restart; the owner unlocked all three.
All 34 top-level archive media thumbnails and nine media samples across three
nested folders returned HTTP 200/image/jpeg. No media or cache regeneration
was needed. No recovery key was requested or extracted. Temporary SSH options
were restored to the original single public key and confirmed started; task
private control files were removed. API/registry superseded objects and exact
VPS `.release57` staging are absent. VM release containers/image, exports,
contexts, tools/caches and both 8 GiB bounded filesystems were removed; exact
`.build/release57`, `.build/release57-builder`, their `.ext4` backing files and
`.build/release57-control` were checked absent. VM disk has 114 GiB free and
available RAM was 3147 MiB after cleanup. A-025 remains pending archive backup
publication acceptance; thumbnail reads and protected unlock alone do not clear it.

# Experimental Pi server

This implementation is isolated on `feature/pi-server`, in
`.worktrees/pi-server`. The owner authorized the trial deployment and app release;
the Pi now owns the three Pi-backed vaults and ordinary Samba files through its
native server. Keep the feature branches unmerged until the permanence decision.
The automatic federation deployed in the ownership cutover was rejected by the
owner. The correction below supersedes its client-discovery design; dated rollout
sections describe historical checkpoints.

## Independent-source correction — November 28, 2026

A saved client source is one FileBeans server or one Google Drive account. Each
server exposes only its own roots, protection, Trash and jobs. The Pi and VPS are
selected independently; the Pi's LAN address and public VPS tunnel address identify
the same Pi server. Configured private peers are used only for explicit transfers
and the existing archive link. Google Drive is not a peer storage root.

Native Google sign-in uses the public iOS OAuth registration for
`com.agawlbb.myfiles`, PKCE, and an App/Share-only Keychain group. The File Provider
cannot read Google credentials and does not create Google domains. Google listing,
media, uploads, and Google-native copies use Google directly. For server/Drive
jobs, the client seals an account grant to the selected server's X25519 key; only
the server streams payloads with Google. Grants are encrypted at rest per job,
expire after 24 hours, renew on explicit retry, and are removed on completion or
cancellation. No web-client secret or server-attached Google account is required.
Transfers between different Google accounts are currently rejected by the client.

The original VPS-library/Pi-originals archive link and ciphertext remain intact.
Mirror coordinate interpretation preserves local root-name collisions without
rewriting the original link or its protection AAD. Pi resource ceilings and its
paced single bulk-transfer budget remain unchanged.

Source through `df59644` passes the full service/SQLite Go suite, Go vet, scoped
copy/move byte checks in both directions for Pi/VPS with a simulated Google API,
archive byte/owner/read-only regressions, and Swift/config/Keychain static checks.
MariaDB integration is skipped: no FileBeans VM `.test-config` exists. TestFlight
preflight `46086597258` passed. Signed app build 55 from `79e42eb` passed
`46089726152`; its IPA SHA-256 is
`24f224c4c856ca984404a8c6dad0c8cdd7247fe6ce76adbeb93fca8f3b3fed7f`.
The signed IPA contains the supplied native OAuth ID, reversed callback scheme
and Google-only Keychain group. Apple validation `46089723836` passed 165 unit
tests and four UI tests on their first attempts. Release checks also caught a full-screen video exit tap intercepted
by native controls; a full-screen tap surface now restores viewer controls while
keeping the same player attached. The original regression passes unchanged.
Upload `46090331733` reused and checksum-verified that exact IPA. Apple build
`a4044cda-b2f3-4906-abfc-723cff65d469` is VALID / IN_BETA_TESTING, not expired, with
automatic notification enabled. No tester/group changes were made. Physical Google
consent and protected-folder acceptance still require device use. No further image
deployment or app build is pending for this correction.

The correction backend is deployed: VPS `filebeans-api:df59644-independent`
(manifest `c76cff6686add3a08267938bc98d4c535207316be51995413f1f0652750ba8db`;
transfer SHA-256 `9adf6fe99477da3e0ffdeb731f9a8cfcce26fb10e7474df6bb46e092546ce1e9`)
and HA `local_filebeans` 0.1.3 (manifest
`e2d26ec12d878529db07b753211b6fc13f9a8bb28305a4fc28c55ac7c5ff3040`).
The prepared Pi registry has 97,082,848 current blob bytes. Its obsolete manifest
and unused old config blob were removed. The VPS media worker remains on release
41. Source builds, tests and preparation stayed on VM-laptop; the VPS only loaded
prepared artifacts and validated/restarted the API.

Live `/v1/fs/sources` returns only VPS on VPS and Samba on Pi; cross-owner browsing
is rejected with 400. VPS lists its two protected folders, Pi its three locked
folders. Pi LAN and public HTTPS return the same server ID/certificate; anonymous
LAN returns 401. Pi startup verifies 512 MiB/no swap/one CPU/128 PIDs. App RAM was
14.5 MB and Core CPU 1.33% at the sample; Supervisor reports healthy/supported.
The authoritative archive-link ciphertext still hashes to
`e628005f4682156549021c8de491a78bb397ebd045de6fe5d49a0ba5354107fd`.
No source bytes or protected registry entries were migrated by this correction.

VPS `.independent-20261128`, transferred exports and superseded API image were
removed after live checks. Pi private-config temporary copy is gone. The temporary
SSH key was removed while preserving the original key; new authentication with
the task key was denied, and private control files were deleted. VM loop staging,
Go caches, contexts, image exports and disposable tooling containers/images were
removed; shared builder state is restored and its worker stopped. The existing
A-025 permission journal remains until migrated protected/archive acceptance after
owner unlock; the retained original and existing drift attention are unchanged.

## Runtime and routing

The Pi runs one HA app containing the Go API and its separate mount broker.
The API is UID/GID 1000; only the broker has mount privileges. HA, Supervisor and
Docker APIs, host PID/IPC and host networking are not exposed. The app maps
`/share` and its own configuration. FUSE and `SYS_ADMIN` are necessary for the
existing protected-folder implementation and the app-scoped resource controls.
AppArmor is disabled for this app, matching the earlier Pi FUSE helper; the
authorized native FUSE installation trial passed with the stated resource caps.

Boot must establish an app-private cgroup: 512 MiB RAM including charged file
cache, zero swap, one CPU equivalent, CPU weight 10, 128 processes, and grouped
OOM termination. It refuses to start if these controls cannot be established.
The API uses `GOMAXPROCS=1`, `GOMEMLIMIT=192MiB`, and embedded SQLite with WAL,
full synchronization, a single connection and a local worker lock. MySQL and
its advisory-lock behavior remain unchanged on VPS installations.

Bulk transfers are serial. Pi chunk reads are 256 KiB, paced to at most 12 MiB/s,
and wait while available host memory is below 768 MiB, one-minute load is at
least 3, or full I/O pressure averages at least 10%. Rclone-based local moves
use one transfer/checker and the same bandwidth ceiling; checkpoints interrupt
them under pressure for later retry. These controls do not constitute a measured
HA responsiveness guarantee. Foreground streaming and Tailscale's separate
process also need observation during the trial.

Pi thumbnails use existing adjacent caches only. Cache lookup on the Pi also
accepts the former Samba 100-nanosecond timestamp revision after trying the
current exact revision; this preserves imported thumbnails after direct-storage
migration without rewriting media or caches. Generation remains disabled.
No thumbnail backfill, archive coordinator, dataset processing or media
normalization runs on the Pi. Archive
links retain their coordinator and media processing on the VPS; bounded storage
operations and verified original publication run on the Pi owner. A separate
server-only archive credential protects these maintenance operations from normal
client tokens. Filesystem and protected-folder operations remain available.

Keep the existing `Samba` source identifier during migration, even though the Pi
uses a local directory. This preserves stored paths and vault identities. Source
names have exactly one owner; peer IDs and `serverId` must remain stable. The
Pi peer owns `VPS` and `drive`; the VPS peer owns `Samba`. Drive credentials stay
on the VPS. Peer URLs and credentials are administrator configuration, never
caller-supplied URLs.

The Pi aggregates source lists, jobs, vaults and Trash, routing requests to the
owner. Remote operation IDs are returned as `peerId~originalId`. Configured
filesystem sources remain visible during peer outages. Pi-local operations,
including unlocking local protected folders, remain usable without the VPS;
remote unlock failures do not undo a successful local unlock. Global locking
reports failure if a remote server cannot confirm it.

Cross-server jobs run on the destination server. The source exports a bounded
snapshot, version-checked chunks and idempotent Trash cleanup. Files stage at the
destination, are checksum-verified and published without replacement. Moves
revalidate source and destination before journaling cleanup; cancellation cannot
remove originals before commit. Retry keeps the same job and submission key.
Pi–Drive transfers stream through the VPS's Drive service, without a client
relay. Neither browser clients nor Apple Files implement this orchestration;
Apple Files still controls transfers it initiates itself.

The trial implementation caps peer/Pi-copy manifests at 4,096 entries per job
and rejects larger selections without publishing them. Pi folder listings use
the same 4,096-entry limit. Large selections must be
split until an on-disk manifest implementation replaces that cap. Google native
documents or downloadable Drive files without a usable checksum are rejected
for peer copies; originals are retained.

Protected transfer records retain existing encryption. Remote protected names
are encrypted using the destination broker's volatile metadata key and are
readable only while the source vault is also unlocked. Both brokers must use the
existing recovery key. No recovery key is saved in SQLite, configuration or logs.

## Local pairing and VPS access

Existing connections need no changes. An optional local URL, certificate
fingerprint and stable server ID enable LAN-first routing for browsing, file
operations, uploads and media. The app probes local capabilities with a short
deadline and caches the route for ten seconds; connection failure falls back to
the existing remote URL. Certificate or server-identity mismatches fail visibly.
Requests are not automatically replayed after a mutation may have reached a
server. Existing job retry retains its idempotency key.

The Pi profile creates a persistent P-256 certificate/private key if neither
exists. The HA app log shows only its public SHA-256 fingerprint and server ID.
Verify those in HA, then enter them with `https://PI_LAN_IP:8095` in the connection
editor. No DNS change or device-wide certificate authority is needed. The same
pairing is stored with the connection for Apple Files and the Share extension.
The certificate lasts five years; replacement requires explicit client
re-pairing. Partial or damaged identities are never silently regenerated.

The remote connection URL must proxy to this **same Pi server**, rather than to
the VPS's own API, so routing preserves server identity. Reuse the existing
Tailscale connection. Configure the VPS reverse proxy to use the Pi certificate
as a trust anchor and `filebeans-pi` as its TLS server name. Copy only the public
certificate to the VPS; never copy the Pi private key. Restrict the Pi port's
tailnet access to the intended VPS/admin peers. A new public route and its exact
certificate configuration must be prepared and validated during authorized
deployment; no port forwarding at the home router is required.

## Packaging and ownership handoff

`companion/deploy/home-assistant` contains the HA app definition, runtime-only
Dockerfile, startup/resource scripts and non-secret example configurations.
Prepare ARM64 API/broker binaries, signed upstream gocryptfs, and pinned rclone
on VM-laptop. Build/publish the image only on explicit request using the shared
VM procedure. HA must pull that image; do not build or install toolchains on Pi.
Adapt example hostnames and replace all token placeholders privately.

During installation, configure root-owned broker configuration and trusted
executable paths. Create `/data/api` with UID/GID 1000 and mode 0700. Run the
explicit `filebeans -config /config/server.json -migrate` maintenance command as
that identity before normal API startup. Boot does not migrate schemas.

For migration, first inventory actual mounts, host RAM/swap, permissions and
existing archive/public-link relationships. Drain affected transfers, finish
incomplete Trash transitions, preserve terminal failed/missing history, lock
the three Pi-backed vaults, and stop the VPS API and broker. Keep both stopped
through the source handoff. Do not restart the old broker with the old registry
or retain a competing writable Samba path after import.

The offline metadata tools are:

```sh
python3 scripts/migrate-pi-vaults.py export --config SOURCE_BROKER_CONFIG \
  --source Samba --output PRIVATE_BUNDLE
filebeans -config SOURCE_API_CONFIG -handoff-bundle PRIVATE_BUNDLE \
  -export-source-state PRIVATE_OPERATIONS -preserve-archive-links
python3 scripts/migrate-pi-vaults.py retire --config SOURCE_BROKER_CONFIG \
  --bundle PRIVATE_BUNDLE --receipt PRIVATE_RECEIPT
# Review the dry run, then repeat retire with --apply.
python3 scripts/migrate-pi-vaults.py import --config PI_BROKER_CONFIG \
  --bundle PRIVATE_BUNDLE --receipt PRIVATE_RECEIPT
# Review the dry run, then repeat import with --apply.
filebeans -config PI_API_CONFIG -handoff-bundle PRIVATE_BUNDLE \
  -import-source-state PRIVATE_OPERATIONS -preserve-archive-links
```

Stage these small mode-0600 files at an exact private, disk-backed location
during the authorized deployment. Move only sealed metadata and the public key
identifier; media stays on Pi. The importer checks parked markers and ciphertext
configuration hashes, preserves source/vault IDs and encrypted locations, and
rebinds device/inode identities to local ext4. It imports into new broker state
and preserves original Trash IDs, expiry and encrypted record bytes in SQLite.
An interrupted import can resume without replacing a different key identifier.
Export commands require the source database and tools; they are deployment
maintenance, not instructions to perform development on production machines.

Update VPS root/broker configuration to remove Samba ownership and add its Pi
peer before restarting it. Keep the two VPS vaults under its broker. Import
SQLite state before starting Pi maintenance, then validate restore and expiry.
After exact target readback, retire the duplicate source Trash metadata in a
guarded transaction matching IDs, kinds, states and body hashes. Keep the
archive coordinator and task journals on VPS; never retire file contents here.
Until then, federation filters source ownership rather than displaying old
source-owned Trash as a VPS source. Retain handoff metadata only while acceptance
or rollback is pending and record its exact location/removal condition.

Archive link/policy rows are mirrored byte-for-byte to Pi SQLite; authoritative
records and task journals stay on the VPS. Import refuses missing or stale policy
revisions. Configure each peer's `vaultSources` map from the existing public vault
IDs so legacy ciphertext keeps its original AAD across the ownership change.
Both brokers must use the same recovery-key fingerprint. Protected records still
require their local and remote vaults unlocked; handoff never exports the key.

HA app 0.1.1 has explicit `maintenance` modes. `initialize` creates SQLite/TLS
identity without starting the broker. `permissions` grants UID 1000 ordinary-file
access, excludes `.filebeans-vaults`, preserves other ACL principals' effective
permissions, rejects hardlinks/symlink traversal and yields under HA pressure.
`import` consumes `/config/sealed-vaults.json`, `/config/retirement-receipt.json`
and `/config/source-operations.json` while the broker remains stopped. Successful
maintenance logs completion and sleeps; stop the app, select `off` and start only
after its configuration and handoff are ready. Normal boot never migrates.

Permission preparation records original ACLs durably before changing them in
`/share/.filebeans-access-<root-path-sha256-first-16>.sqlite`, root-private and
limited to 64 MiB. `prepare-pi-access.py --config CONFIG --restore` can restore
that before-state with inode checks while writers are stopped; changed identities
refuse recovery. Remove the exact journal after verified rollout acceptance.
It contains ordinary-file permission metadata only; vault stores are excluded.

Host swap can stay enabled: the broker/API require a kernel cgroupfs proof of
their current process's zero-swap limit. Ordinary files and symlinks cannot fake
that proof. The cgroup control files remain root-writable; the API can read them.
Python is a runtime maintenance dependency only; no compiler is installed on Pi.

The isolated Pi probe found that an ordinary share's default ACL can otherwise
reach a newly created opaque vault store. App 0.1.2 creates that store privately,
strips inherited access/default ACLs on creation, and still refuses unsafe existing
stores. Native filesystem consumers use the same forced reader UID/group as CIFS
consumers, so migrated ciphertext remains readable through the broker's FUSE mount.

The archive peer protocol streams at most 256 KiB at a time with an exact source
length/disk-space ceiling and full checksum readback before exclusive publication.
Original cleanup follows successful publication and cached-thumbnail delivery.
The original's length, rather than the encoded-output limit, bounds raw staging.
All Pi bulk source reads, archive writes and hash verification share the 12 MiB/s
budget and HA pressure gates. Trash pairing, restore and expiry use destination-
owned idempotent receipts. Coordinator link changes synchronize durable read-only
policies before success; periodic synchronization repairs interrupted delivery.

Rollback must stop Pi writers first, reconcile any trial changes and new Trash
state, then restore sole VPS ownership. Reinstalling the old registry alone is
not a valid rollback after Pi writes. Never overwrite or reinitialize ciphertext.

## Verification and trial acceptance

Synthetic Go tests cover SQLite persistence/uniqueness, peer copy/move and retry,
source-change rejection, TLS pin mismatch, operation-owner routing, offline local
discovery, duplicate destinations, bounded listings, identity persistence and
Trash-state handoff. Python tests cover sealed-registry handoff, local identity
rebinding and refusal of changed/unparked/symlinked storage. Swift route and legacy
connection tests are included; static syntax/project checks run on VM-laptop.

Before a trial can be accepted, an explicitly requested Apple build must compile
and run the client tests. Verify pinning, local-network permission, Wi-Fi/cellular
switching, uploads, seeking and Apple Files on a physical device. Verify FUSE
ownership and startup resource controls with synthetic storage before importing
real vault metadata. Google Drive peer transfers need an isolated authorized
account test, including interruption and moves; synthetic local peer tests do
not establish live Google behavior.

Measure an idle HA API latency baseline, then sustained ordinary and encrypted
local/peer transfers. Require p95 below one second with at most 250 ms regression,
no HA restarts/OOM events and no missed observed automation actions. Confirm
pressure pausing, resource ceilings and reboot/reconnect recovery. Reduce limits
or reject the trial if these criteria fail. Measure physical storage contention
and Tailscale overhead as well as FileBeans process statistics.

No merge is authorized by testing, installation or a successful trial. The owner
must explicitly decide this design is permanent before merging the feature branch.

### VM verification, November 27, 2026

The Go suite passed with only
`TestRcloneCriticalErrorsAreReportedAndRedacted` excluded: that existing test
requires rclone 1.76.0's JSON critical log, while the installed distro version
prints plain text and the pinned upstream download returned HTTP 403. The
single-file/directory rclone transfer tests passed with the installed binary.
Focused node/peer/protection tests also passed with Go's race detector. ARM64
test packages cross-compiled; they were not executed on a Pi. Two Python
migration tests and syntax/configuration validation of all 75 Swift sources
passed. Apple SDK compilation, physical-device checks, synthetic FUSE/HA app
installation, real Drive peer transfers and HA load acceptance remain pending.

Tests ran in a disposable VM container with two CPUs, 2 GiB RAM/no swap, 256 PIDs
and a separate 3 GiB disk filesystem for tool scratch and caches. No production
API mutations, migration, image build, mobile build or deployment was performed.

## Held image and app builds — November 27, 2026

The owner requested image preparation and an app build while withholding deployment
and TestFlight upload. Both images and the signed app were built from runtime
source `ee576bd1bd70866edf618e8617638ca389224aec` on `feature/pi-server`.
The branch remains unmerged; these builds do not authorize permanent cutover.

Prepared VM deliverables are in `.build/prepared-pi-ee576bd/` in this worktree:

| Deliverable | Identity |
| --- | --- |
| `filebeans-pi-ee576bd-arm64.tar` | Linux ARM64; `filebeans-pi:ee576bd`; manifest `sha256:72fe3205c1ebe4580b09db9acd14b94bd79678209db2eb8c84caa9dd454f2870` |
| `filebeans-api-ee576bd-amd64.tar` | Linux AMD64; `filebeans-api:ee576bd-pi-peer`; manifest `sha256:dbf90243a6ca1217466b6a4f74c3a22f8706fe7290222d7b1b0dc86691ff06d8` |
| `myFiles-build-54.ipa` | Signed version 0.1.0, build 54; SHA-256 `01e5a882e51dfb6ef976a6c0ebe5c5b5bdbbf95814f76a961a1a8a78041bd3bb` |
| `manifest.json` and image `.tar.json` files | Exact source, sizes, archive/config/manifest digests, verification and hold status |

The [signed build workflow](https://github.com/agawlbb/fileBeans/actions/runs/46044466696)
completed successfully in `operation=build` mode. Its TestFlight upload step was
skipped and runner signing-credential cleanup succeeded. The retained IPA has
both File Provider and Share extensions and build number 54. GitHub artifact
`myFiles-signed-54` expires December 4, 2026; the verified VM copy is held for the
later requested upload. [Simulator validation](https://github.com/agawlbb/fileBeans/actions/runs/46044470531)
completed successfully. All four new routing tests passed on the first attempt;
the playback seek and photo/fullscreen gesture tests each passed after a retry.
This clears Apple SDK compilation and simulator validation; physical-device
media/permission checks and real Pi/FUSE/Drive/HA-load acceptance remain pending.

Both image archives were built using the canonical VM-local builder. The helper
now supports explicit ARM64 selection and a bounded task cache under its existing
lifecycle lock. Seven helper tests passed, including restoration after a failed
build. Preparation and builder state each had separate 8 GiB ext4 disk ceilings;
the final six deliverables were copied/readback-verified under a 512 MiB aggregate
write ceiling. The final set is about 309 MiB; the manifest records the immutable artifact sizes.

Actual image smoke tests passed with no published ports or production mounts:
TLS identity pinning, unauthenticated rejection, unified source discovery, a
135,000-byte Pi-to-VPS server copy, and a reverse VPS-to-Pi move with source Trash.
The Pi API ran under ARM64 emulation; the VPS API ran natively. Both API containers
had one CPU, 512 MiB/no swap, 128 PIDs and read-only image filesystems; neither
restarted nor suffered an OOM. These checks did not start the HA app's privileged
boot/FUSE broker or establish real Pi/HA load acceptance.

Official rclone 1.76.0 binaries were obtained from the GitHub release and verified
against its checksum manifest; the existing AMD64 checksum pin also matched.
The previously excluded rclone critical-log regression then passed. Pi gocryptfs
2.6.1 passed its upstream signature and the already recorded ARM64 binary checksum.
No signing private key, recovery key or production configuration is in the images.

The disposable image tags, smoke network/containers, tool downloads, synthetic
keys/data, contexts, Go caches and both staging/cache filesystems were removed.
The builder is stopped and its original shared state is restored. Only the six
prepared deliverables remain for explicit deployment/upload or rejection of this
trial. No registry publication, production deployment, TestFlight upload or merge
was performed.

## Initial authorized rollout — November 27, 2026 (historical checkpoint)

The subsequent instruction authorized deployment and app release, retaining the
unmerged feature branch. Signed build 54 was uploaded without rebuilding through
[release run 46055923673](https://github.com/agawlbb/fileBeans/actions/runs/46055923673).
The workflow verified its exact IPA checksum, bundle ID and build number before
upload. Apple build `7ddc3b48-519c-4a0a-b1b6-cc3f590a548b` is VALID and
IN_BETA_TESTING, with autoNotifyEnabled true. The existing internal group Joe
retains access to all builds. No testers were added or removed.

The Pi image is published as
`files.coffeybean.com/filebeans-pi-aarch64:0.1.0` using read-only OCI distribution
through the existing Caddy server, with manifest `72fe3205c1ebe4580b09db9acd14b94bd79678209db2eb8c84caa9dd454f2870`.
`prepare-pi-image-registry.py` verifies blob sizes/digests with a 96 MiB aggregate
write limit. No separate registry daemon or Pi build toolchain was installed.
Supervisor successfully pulled and installed `local_filebeans` on HAOS 18.5,
Pi 4 / ARM64. Actual startup established 512 MiB/no swap, one CPU and 128-process
limits. With no production configuration installed, the broker correctly refused
startup. The app was stopped and its boot mode set to manual; this is an installed
image, not a functioning storage server. Core was not restarted. Supervisor stayed
healthy; available memory was 2.22 GiB before and after this bounded startup check.
This does not establish FUSE, transfer-load or HA responsiveness acceptance.

Production ownership has not changed. The VPS remains on its existing release-41
image and Samba connection. Raw operation inventory found a sealed archive link
between Samba vault `639f8ece321e9342de4c57ba78ddea28` and VPS vault
`73b9b29e822abb8476c4ababf7de0043`, with an attention task reporting linked-folder
changes outside FileBeans. This implementation does not federate archive sync.
The owner was asked to choose between retaining that relationship and extending
cross-server archive support, or unlinking while preserving all files. No unlink,
data move, registry retirement, source-state export or API/broker cutover occurred.
Failed source transfers and unresolved Trash records also require reconciliation
before the existing exporter permits handoff. Their records remain intact.

Remaining rollout: resolve the archive decision and relevant failed operations;
prepare explicit SQLite initialization and sealed registry/Trash import through
the supported HA app maintenance path; configure private tokens and ownership;
deploy the VPS peer image and Pi HTTPS route; verify FUSE, Drive, server-side moves
and HA load acceptance. The existing Tailscale path is active, but no public Pi
API route has been enabled yet. Physical-device LAN/remote acceptance remains
manual after the service is ready.

After Apple processing and Pi image delivery, the local IPA and Pi image archive,
the signed GitHub artifact, duplicate registry preparation, extracted maintenance
binary, transfer script and temporary SSH key/directory were removed. SSH options
were read back with the original authorized key preserved and the task key absent.
Only the prepared AMD64 image and small manifest/checksum metadata remain in
`.build/prepared-pi-ee576bd/` for the pending VPS deployment. Remove them after
verified deployment or cancellation. The VPS current read-only registry files
remain operational while the installed HA app references that image. There are no
VPS staging archives or migration exports. Shared registers record these purposes.


## Archive-preserving continuation — November 27, 2026 (pre-cutover checkpoint)

The owner chose to keep the archive link. Runtime source `a637aff` implements
server-only authenticated archive federation: the VPS retains encoding, archive
coordination, sealed links and task journals; Pi owns the originals and paired
Trash. Live policy mirroring preserves write guards and legacy vault identities.
The exact encrypted link remains unchanged; no unlink or user-file move occurred.
The pre-existing linked-folder drift attention task is not automatically repaired.
Two historical failed, uncommitted Samba-to-Drive moves were cancelled only after
confirming their source/quarantine and private destination staging were absent;
their already-trashed published Drive files were preserved. Terminal failed/missing
Trash history is now accepted by the sealed handoff exporter.

Prepared ARM64 HA image 0.1.2 was published, pulled and installed successfully.
Manifest: `569f21a9c2837827d76f809caa92690b34d914b68ecf4234aa9fa81c4419ebdf`.
Explicit initialize/permissions modes operated only on a disposable probe root.
The actual broker created an encrypted folder through native ext4/FUSE, copied
64 MiB entirely on Pi, verified SHA-256
`43628cf4c307b03bbcddff0cb7d4e81fe5cb4c100f2d95d4b5181e5009a97331`,
then locked, unlocked, re-read and re-locked successfully. Copy plus initial
readback took 50 seconds with conservative pacing. This is small-fixture acceptance,
not a sustained throughput claim. During checks Core memory was about 902 MB,
CPU samples 1.08–4.85%, available memory about 2.2 GiB, and Supervisor remained
healthy/supported. The child cgroup caps were 512 MiB/no swap/one CPU/128 PIDs;
host swap and Core configuration were unchanged. This test caught and fixed
inherited default ACLs on new private stores before production data was touched.

The prepared VPS image `filebeans-api:a637aff-pi-peer` was transferred with verified
archive SHA-256 `6e11be209a37cd0c9d9555120d309d9f4853251155a4f3c552161e42ef2730ff`
and loaded, but not started. Manifest:
`80da8a1a847becfc91100477024a881946475b097567d82411e4f3808e9d1509`.
Full Go service tests passed in that exact packaged runtime, with source fixtures
and pinned rclone supplied by the VM test harness. No iOS source changed;
TestFlight 54 remains released, with no new Apple build/upload required.

Production cutover is pending explicit approval to grant API UID 1000 ordinary-file
read/write ACLs under `/share/fileBeans`. Automatic approval review required
separate authorization for that persistent access change. The helper excludes
private vault stores, preserves other principals' effective permissions, limits
traversal and writes a bounded before-state journal supporting reversal.
The isolated probe permission step passed after an exact root/count guard proved
it could not reach the real share. No real-share permissions were changed.
After approval: configure production peers, initialize/import sealed metadata,
retire the VPS Samba registry ownership, remove its writable Samba root, start
Pi/VPS services and enable `/pi/companion` with the exact Pi public TLS certificate.
Then verify ordinary/Drive/server transfers, real protected folders after owner
unlock, and LAN/remote device routing. No recovery keys should be sent in chat.

The Pi app is stopped/manual with no probe/production config installed. The VPS
continues its unchanged release-41 API/broker and Samba ownership. No migration
exports, registry retirement or public Pi API route exist yet. The test root,
synthetic recovery key, ACL journal, temporary SSH authorization/control directory,
VM tools/containers/images, 8 GiB bounded scratch, old held image deliverables and
all image transfer archives/scripts were removed and exact paths checked absent.
The VM builder is stopped with its original shared mount restored. VPS staging
was removed; registry hosting retains only installed image 0.1.2 (86,706,898 bytes).
The loaded, not-running VPS image remains only for the pending authorized cutover;
remove it after deployment rejection/cancellation or if superseded. Both source
feature branches remain pushed and unmerged by owner instruction.


## Completed ownership cutover — November 27, 2026

The owner explicitly approved UID 1000 ordinary-file ACL access and instructed
completion. HA `local_filebeans` 0.1.2 is running with maintenance off and boot auto;
VPS `FileBeans` runs prepared `filebeans-api:a637aff-pi-peer`. Its existing worker
remains on release 41 because its media protocol is unchanged. The VPS API keeps
2 GiB/no swap/128 PIDs; Pi startup verifies 512 MiB/no swap/one CPU/128 PIDs.
No new image or Apple build was necessary. TestFlight 54 was rechecked VALID,
not expired. Both source feature branches remain unmerged.

The access helper visited 225 ordinary entries (281 ACL updates) and excluded
ciphertext stores. Three empty protected-folder markers had their journaled
original 0755/no-ACL permissions restored on their original inodes before import.
Effective UID 1000 read/write/traverse access was checked, and the private-store
parent remains non-writable by that UID. The source broker was stopped and all
five vaults confirmed locked/parked before registry retirement. Three sealed
Samba registry entries were imported on Pi with native device/inode identities;
the two VPS entries remain with its broker. Source IDs and sealed locations are
preserved; peer-facing UI IDs are namespaced by their owner as designed.

All 73 source Trash records and the sealed link/policy were imported into Pi
SQLite. A separate never-started maintenance registry enabled readback-import
verification of every existing Trash body; it was then removed. Exact ID, kind,
state and SHA-256 predicates matched all 73 obsolete VPS Trash copies before a
single guarded transaction retired those metadata rows. No file contents were
deleted by this metadata retirement. Unified Trash now has 41 visible entries and
zero duplicate normalized IDs. The original archive-link ciphertext for
`785f4a40ec6201758b8f99dec9e131a6` still hashes to its pre-cutover policy revision.
Its authoritative coordinator/task journals remain on VPS.

VPS API configuration and Compose no longer provide a writable Samba root or
mount. Its Samba refresh timer is disabled, and the tracked mount helper skips
Samba when no Samba root is configured. The existing host `/samba` aliases and
`/mnt/filebrowser` mount remain in place: automatic review rejected unmounting
without ruling out non-Docker consumers, so these pre-existing mounts were
preserved. FileBeans no longer uses them. Base Compose was reconciled with its
existing live namespace/security settings; use both documented overlays.

### Connection values

- Remote server URL: `https://files.coffeybean.com/pi/companion`.
- Local server URL: `https://192.168.12.246:8095`.
- Server ID: `filebeans-pi`.
- Local certificate SHA-256:
  `1b3b070668d4e829c84532d5c7ade8e3a3010282c4e0003fafbd26f838eeeae5`.
- API token: the private `token` value in HA
  `/app_configs/local_filebeans/server.json`; enter it in the connection editor
  when changing the server URL. The server-only archive key is separate.

The app tries the pinned LAN connection first and uses the remote route when
local networking is unavailable. The existing VPS connection remains functional
with unified sources; switch to these Pi values for direct Wi-Fi access.
Caddy's `/pi/companion/*` route uses the existing Tailscale tunnel, trusts only
Pi's public certificate at `/opt/vps/caddy/conf/filebeans-pi.crt`, and verifies TLS
server name `filebeans-pi`. No home-router port forwarding or Pi private-key copy.

Live checks passed pinned tailnet and authenticated LAN identity, public Pi/VPS
identity, unified source/vault discovery, Drive root listing through Pi/VPS,
anonymous/invalid-token 401 rejection, and server-only archive RPC 403 rejection
for an ordinary API token. Caddy/Compose validation passed. API restarts/OOM are
zero. HA Supervisor remains healthy/supported; Core CPU sampled 0.91%, Core RAM
850 MB, Pi app RAM about 13–17 MB, and available memory about 2.25 GiB.

Protected folders are cold/locked after restart. An in-app unlock was requested;
no key was requested in chat or extracted. Root writes correctly return the
conservative archive-policy guard while the sealed policy is locked, so the
post-cutover copy/move fixture attempt created no files. The preceding isolated
real-Pi 64 MiB encrypted copy/checksum/lock/unlock/readback and two-node image
transfer tests passed, but migrated-folder reads, a production server-copy run,
queued-original backup completion and physical-device route selection await that
unlock. Current archive counters show one pending original and a linked-folder
drift attention record. Preserve the retained VPS original and review the drift
after unlocking; no automatic unlink or guessed inventory repair was performed.
The drain marker is removed so queued work can resume when keys are available.
These are acceptance/activation checks, not another image deployment or app build.

HA recovery must include `local_filebeans` app data (SQLite, TLS identity and
broker registry/key identifier), its app configuration, and the shared ciphertext
files. Existing VPS full-root capture includes its private peer configuration,
remaining broker state, SQL backup policy and Caddy public certificate. Git alone
does not restore these runtime identities; no backup schedule was changed.

VPS handoff staging `/opt/services/filebeans/.pi-handoff-20261127`, transfer files,
validation cache and drain marker are absent. VM `.build/cutover-20261127`, its
emulator/restorer/metadata copies/scripts, and the temporary SSH/control directory
were removed. Task SSH access was revoked and verified denied; the original key
remains. Pi verification-only metadata, import files and temporary executables
were removed. The sole retained task artifact is the private 77,824-byte ACL
before-state journal `/share/.filebeans-access-62877acfd70fe72e.sqlite`, needed
until the migrated protected folders/archive checks pass after owner unlock;
remove then, or after a verified cancellation restores permissions. It is not an
indefinite rollback archive. Shared register A-025 records this pending check.


### Archive confirmation and legacy journal recovery (2026-11-28)

A job's hidden VPS `original` is its input before that particular encode. It may
already be an encoded derivative from an earlier pass. It is not necessarily
the archive's master original, and its hash must not be equated with the visible
encoded VPS file or assumed to match every existing Pi version.

Pi 0.1.7 carries the native API ownership fix: protected mounts report control
UID 1000, enabling timestamp preservation. The peer protocol reports permission
failures distinctly from unavailable storage. VPS API `1a3a2a3-archive` verifies
an existing archive original's length/hash before transmitting bytes. A matching
copy is reused, a missing copy is published with checked readback, and a differing
copy remains a conflict rather than replacing the archive original. Backed-off
copies receive one recovery attempt when first visible after startup/unlock.

The VM-prepared `filebeans-archive-audit` tool defaults to read-only authorized
operation metadata. Its explicit `-reconcile-plan` mode requires a link ID and
an exact list of journal IDs. It obtains the worker advisory lock; production
workers must be stopped for this operation. Unselected work remains untouched.
An unstarted discovery intent can be cleared only if its source version is still
unchanged, no staging exists, and its archive counterpart now exists.

For legacy retained bytes differing from the canonical Pi file, maintenance
preserves both. The retained bytes are copied without overwrite to the same
protected Pi vault at
`pics/.filebeans-stage/archive-recovery/<job-id>/<original-basename>`.
`details.json` records the retained SHA-256, canonical path/identity/revision,
visible library path and reason. File checksum/readback, durable metadata and
unchanged canonical identity are required before the VPS retention is released.
These are live user recovery versions, not disposable deployment staging; they
must not be removed by build/task cleanup. The canonical Pi originals and visible
encoded VPS files are preserved. No new thumbnail generator or Apple build is
needed for this server repair.


Delivery acceptance: VPS 83342fc / Pi 0.1.7; both queues zero and scopes independent.
294 inputs completed ordinary verified backup, 83 differing inputs preserved in
protected recovery storage, 92 stale discovery intents cleared. The protected
`pics/.filebeans-stage/archive-recovery/index-20261128.json` catalog lists the
83 exact filenames/checksums (1,505,441,079 bytes). No canonical Pi original was
overwritten; the library remains 749 files. See RELEASING.md for artifact and
cleanup details. Old permission journal A-025 removed; user recovery data A-029
remain deliberately protected. No Apple build or Pi renderer tools added.


## Filename pairing deployment and redundant-copy removal — November 28, 2026

Owner explicitly authorized server deployment and deletion of the redundant copies.
VPS now runs `filebeans-api:df5115d-filename`, manifest
`sha256:1248d6f7a152538cb2a922a32ae9d4edecd8506982a47592a30f6568df2469c6`,
API SHA-256 `7fed8192dd4a26e83540b7a1feb0c4ceb7ba574ef73f17f13644ad2dd402bc4f`.
The VM-built transfer SHA-256 was
`9b9bca0f61ae624b4b0c5300825848b71ac8409e904573d9a243083c095c7009`.
Pi app 0.1.8 is installed/started, manifest
`sha256:8a0b782713045c539bf74c486e65e95d444d3bed43f981f268e32d26a9d85a5a`,
API SHA-256 `f5aec1333f0b324871d3f500f82761586e734e767e3956c3139cc27e47549845`.
It reuses the 0.1.7 runtime, broker and tools. Four superseded binary-only layers
were validated by their exact tar members and omitted from the prepared OCI image;
current registry is 14 blobs / 117,616,455 bytes, below its 128 MiB packaging cap.
The pinned Caddy manifest route was updated after the initial pull returned 404;
Supervisor update succeeded after the exact manifest was served and verified.
Pi rendering remains disabled, without new video/PDF tools. API limits remain
2 GiB/no swap/128 PIDs; Pi startup confirms 512 MiB/no swap/one CPU/128 processes.
Supervisor is healthy/supported; Pi app used 16,896,000 bytes at 1.39% CPU,
HA Core 829,181,952 bytes at 3.49% CPU. The release-41 media worker is unchanged.

Before restarting Pi, the exact recovery catalog was validated: 83 distinct job
copies, 1,505,441,079 bytes, 59 canonical paths. Every copy size and details record
matched; each canonical master still had its recorded size/timestamp. Only the
83 catalogued files, their details.json files, empty job directories, index and
empty `pics/.filebeans-stage/archive-recovery` root were removed with individual
remove calls. All 59 masters still stat successfully afterward; the visible Pi
inventory remained 749 and recovery-root stat returned missing. No new full-media
hashing or transfer was performed. The nine previously unverified masters remain
accepted as original at the owner's instruction. Archive-link ciphertext remains
`e628005f4682156549021c8de491a78bb397ebd045de6fe5d49a0ba5354107fd`.

Both public APIs respond with their independent server IDs/scopes. VPS has zero
restarts/OOM events. Pi restart locked its protected folders; VPS is still unlocked.
At this restart boundary 174 pending work entries appeared (173 waiting and one
change), with zero pending originals/problems. Deployment drain remains installed
at `/opt/services/filebeans/media-runtime/drain`: no encoder child was running.
Owner Unlock has been requested. Final acceptance must verify the filename guard
retires existing-counterpart waits, remove drain, then confirm quiet queues and
749 masters. Do not declare archive acceptance complete while this remains pending.

Superseded VPS 83342fc image, old Pi manifests/five unreferenced registry blobs,
image transfers and operation-local config copies were removed. Temporary Pi SSH
options were restored, app restarted and task-key authentication confirmed denied;
private control files removed. VM preparation container, exports, SDK/caches and
`.build/archive-filename-deploy` plus both bounded filesystems/backings are absent;
shared builder state restored and worker stopped. VM has 114 GiB free and 3599 MiB
available RAM. Pending live check alone retains the bounded VPS filesystem
`/opt/services/filebeans/.filename-deploy` and backing `.filename-deploy.ext4`,
containing only the 9,228,482-byte prepared read-only status helper plus lost+found.
Remove that helper, mount/backing and drain after successful owner-unlock acceptance.
All feature branches remain unmerged; no mobile build/release was requested or run.


Owner-unlock acceptance completed November 28, 2026: both servers report unlocked
with the shared group unlocked. Deployment drain was removed; all 174 restart-boundary
pending entries cleared through normal processing. Both servers now report zero
pending originals, changes, work and problems. Pi visible media inventory remains
749 and recovery-root stat returns missing. The media worker has only its normal
init/server processes, with no encoder child. The prepared status helper, bounded
VPS `.filename-deploy` mount and `.filename-deploy.ext4` backing were removed;
all exact staging paths and deployment drain checked absent. Deployment and
redundant-copy deletion are finished and verified; no mobile build is needed.


## Read-only HA load review — November 28, 2026

Owner authorized testing before deciding whether the two-server setup is permanent.
Three sequential existing-file reads discarded all received bytes: ordinary LAN
672 MiB/60.345s (11.136 MiB/s), protected LAN608 MiB/61.354s (9.910 MiB/s),
protected public VPS/Tailscale tunnel320 MiB/70.396s (4.546 MiB/s). Each used one
stream,32 MiB HTTP ranges,12 MiB/s pacing and768 MiB total ceiling; the60-second
loop target allowed the final range to finish. No upload/move/delete, automation
trigger, service/config change, restart or merge. Pi LAN TLS certificate pinned;
credentials passed only in a pipe into a128 MiB/no-swap/0.5CPU/32PID VM container.
No media/credentials were stored on the VM or VPS.

Authenticated HA template reads through the connector had these p95 round-trip
results (nearest-rank, one request at a time):

| Phase | Samples | p50 ms | p95 ms | Maximum ms | Failures |
| --- | ---: | ---: | ---: | ---: | ---: |
| Initial idle |20|589|877|994|0|
| Ordinary LAN |35|691|1230|1350|0|
| Protected LAN |35|644|955|2267|0|
| Intermediate idle |20|585|913|1901|0|
| Protected tunnel |16|1402|1990|1990|0|
| Final idle |20|579|678|849|0|

The connector includes remote transport/tool overhead, so these are not internal
HA processing times. A supplemental direct LAN GET `/auth/providers` measured
30 successful HTTP200/valid-provider-list responses during tunnel load: median
20.303ms,p95 53.207ms,max69.969ms. Matching post-load idle:30 successes,median
14.519ms,p95 25.109ms,max50.804ms. Initial supplemental attempts rejected the
installed HA response object because the probe expected a top-level list; those
invalid validation samples were discarded. The corrected probe validates the
`providers` list inside the response object. Direct checks exercise HA's public
auth-provider endpoint, not the authenticated template API or physical automations.

Sampled FileBeans RAM peaked55,418,880 bytes; CPU samples4.89–19.86% during LAN
reads and13.07% during tunnel load. Tailscale tunnel sample17.08%CPU/46,567,424
bytes. These are sparse samples, not guaranteed peaks. HA stayed healthy/supported,
zero active repairs, unchanged recorder run06:31:50UTC. Available host log window
had no OOM/thermal/undervoltage/I/O warnings; no new resource-related Core errors.
Existing unreachable Tuya fan errors continued. Host temperature, pressure,
Core CPU/RAM and effective cgroup counters were not directly sampled under load.
Cache effects are possible. No write-load, LAN server-to-server copy, pressure
pause injection, reboot/reconnect, or physical automation outcome was tested.

Result: bounded read-load testing found no local HA responsiveness/health failure,
but strict end-to-end connector p95 criteria fail for ordinary LAN and tunnel
runs. The tunnel result is consistent with network/remote-path contention, not
proof of its cause. Do not claim unrestricted permanence acceptance: remote
response degradation remains a decision/diagnostic item. All test containers,
`.build/ha-performance-readtest.py`, `.build/ha-http-probe.py` and original
`/tmp/filebeans-ha-readtest.py` are removed; no test artifacts retained.
