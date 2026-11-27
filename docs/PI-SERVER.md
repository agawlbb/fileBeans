# Experimental Pi server

This implementation is isolated on `feature/pi-server`, in
`.worktrees/pi-server`. Do not merge it or change the running services until the
owner decides to trial it. Image/mobile builds, publishing and deployment remain
separately authorized operations. No live storage has been migrated.

## Runtime and routing

The Pi runs one HA app containing the Go API and its separate mount broker.
The API is UID/GID 1000; only the broker has mount privileges. HA, Supervisor and
Docker APIs, host PID/IPC and host networking are not exposed. The app maps
`/share` and its own configuration. FUSE and `SYS_ADMIN` are necessary for the
existing protected-folder implementation and the app-scoped resource controls.
AppArmor is disabled for this app, matching the earlier Pi FUSE helper; this
packaging still needs an installation trial, rather than assuming isolation is
equivalent to an unprivileged app.

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

Pi thumbnails use existing adjacent caches only. No thumbnail backfill, archive
coordinator, dataset processing or media normalization runs on the Pi. Archive
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
The exported source Trash rows remain on the VPS for rollback; remove those
exact rows only after acceptance, with a separately reviewed maintenance step.
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

## Authorized rollout — November 27, 2026

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
