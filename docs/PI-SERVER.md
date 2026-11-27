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
sync, dataset processing or media normalization runs on the Pi. Filesystem and
protected-folder operations remain available. Archive relationships involving
Pi storage must be explicitly unlinked before cutover; this version does not
federate the archive worker. VPS-only processing remains on the VPS.

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
Trash recovery, explicitly resolve relationships the exporter refuses, lock
the three Pi-backed vaults, and stop the VPS API and broker. Keep both stopped
through the source handoff. Do not restart the old broker with the old registry
or retain a competing writable Samba path after import.

The offline metadata tools are:

```sh
python3 scripts/migrate-pi-vaults.py export --config SOURCE_BROKER_CONFIG \
  --source Samba --output PRIVATE_BUNDLE
filebeans -config SOURCE_API_CONFIG -handoff-bundle PRIVATE_BUNDLE \
  -export-source-state PRIVATE_OPERATIONS
python3 scripts/migrate-pi-vaults.py retire --config SOURCE_BROKER_CONFIG \
  --bundle PRIVATE_BUNDLE --receipt PRIVATE_RECEIPT
# Review the dry run, then repeat retire with --apply.
python3 scripts/migrate-pi-vaults.py import --config PI_BROKER_CONFIG \
  --bundle PRIVATE_BUNDLE --receipt PRIVATE_RECEIPT
# Review the dry run, then repeat import with --apply.
filebeans -config PI_API_CONFIG -handoff-bundle PRIVATE_BUNDLE \
  -import-source-state PRIVATE_OPERATIONS
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
