# Temporary Home Assistant archive extraction

Prepared 2026-11-24 for the aarch64 Raspberry Pi 4 running HA OS 18.5.
This is a local, one-time recovery helper, not a FileBeans server or media worker.
The FileBeans app and VPS API are unchanged.

## Installed configuration

- HA app: `local_filebeans_extract`, manual-only boot, no published ports and no
  Supervisor/Core/Docker API access. Prebuilt base:
  `ghcr.io/home-assistant/base:3.24-2026.11.0`. No builds or package installs on Pi.
- Root-owned deployment: `/local_apps/filebeans_extract` (about 11 MiB).
  Includes `worker`, signed upstream gocryptfs 2.6.1 ARM64, startup scripts and
  non-secret job descriptions. Remove after the real extraction is verified.
- Private control directory: `/share/.filebeans-extract-control`, mode 0700;
  socket mode 0600. Bounded `status.json` contains action/state and summary counts.
  No HTTP listener. The worker processes one request at a time.
- The s6 startup hook mounts cgroup2 inside the app's own cgroup namespace,
  moves only the app's processes to a child cgroup, and enforces 512 MiB RAM,
  zero swap, 64 PIDs, and one CPU core. Inherited parent limits still apply.
  Startup fails before the worker runs if any control cannot be established.
  HA's app stats report the parent limit; the startup log records child limits.
- `/dev/fuse`, `SYS_ADMIN`, and disabled AppArmor are scoped to this temporary
  app, with read-only local-app definitions and writable `/share` mappings.
  No HA host shell, Docker socket, full hardware access, or HA restart is needed.

## Confirmed operation

The user confirmed **Pic archive → pics** on Samba:

- Source vault: `b4188719242de0d6ef445a55702c1333`.
- Source file: `pics archive.tar.gz`, 44,861,578,484 bytes (~41.8 GiB).
- Destination vault: `639f8ece321e9342de4c57ba78ddea28`, root `files` directory.
- The VPS metadata listing showed the destination empty on 2026-11-24.
- Both vaults reside in `/share/fileBeans/.filebeans-vaults` on local ext4.

Initial setup used synthetic keys only. The user subsequently supplied an
existing Pi-local recovery-code file; the helper is performing a read-only
inspection. The code is canonicalized in memory without changing or copying
the user file. No real extraction has run.

## Run procedure (pending secure key entry)

1. Start `FileBeans temporary extraction` from HA Apps if stopped. Check its log
   for all four limits and `helper ready`. Status, without a key:
   ```sh
   /local_apps/filebeans_extract/worker client /local_apps/filebeans_extract/status.json
   ```
2. The user explicitly chose to keep protected VPS folders available. Do not
   apply the global protection lock or change the VPS protection state. The user
   will not access or write the Samba source/destination during the job; viewing
   unrelated protected VPS folders is allowed. The replacement automatic
   coordinator never calls the VPS and submits extraction only after inspection
   passes. Source mounts remain read-only and destination publication refuses
   overwrite.
3. In the HA Terminal (not chat), run:
   ```sh
   bash /local_apps/filebeans_extract/key-entry.sh /local_apps/filebeans_extract/inspect.json
   ```
   Enter the recovery code at the hidden prompt. It travels through a private
   local socket and a gocryptfs stdin pipe, never argv, environment, logs, or a
   saved key file. Both mounts are read-only for inspection. This scans the
   entire gzip stream on the Pi and reports file/directory counts and expanded
   bytes. No media travels through the VPS or VM. Review the result before
   proceeding; a rejected archive must not be partially imported or silently
   renamed to bypass the filename policy.
4. After a successful inspection and confirmed Samba access coordination, run:
   ```sh
   bash /local_apps/filebeans_extract/key-entry.sh /local_apps/filebeans_extract/extract.json
   ```
   Enter the code again. The source stays read-only. The destination must still
   be empty. Extraction validates again, writes into an encrypted staging
   directory inside the destination vault, fsyncs files, verifies every file by
   SHA-256 readback, and promotes top-level entries using RENAME_NOREPLACE.
   The source archive is preserved. Status can be read separately:
   ```sh
   cat /share/.filebeans-extract-control/status.json
   ```
   The job continues if the terminal connection disconnects; do not resubmit an
   active job. `running` is not proof of liveness after an app/Pi restart.
5. Check the final summary and clean unmounts, then stop the helper. Open via
   the normal FileBeans client and verify counts and representative nested
   files. Confirm that the original archive remains and staging is absent.
6. Uninstall only this app, remove its exact local-app directory and private
   control directory, and refresh HA's app store. The base image is shared;
   never broadly prune images. Remove it only if proven task-exclusive and
   unreferenced. Preserve the user's vaults, source archive, and SSH credential.
   Resolve the shared housekeeping entry after these checks.

An interruption/power loss may leave an encrypted
`files/.filebeans-extract-incomplete-*` directory or a partially promoted import.
Inspect the exact destination before resuming; never delete or retry blindly.
Normal errors clean staging, but promotion rollback is not a filesystem-wide
transaction. The worker deliberately refuses nonempty destinations.

## Bounds and validation

Only regular files and directories are accepted. Absolute paths, parent traversal,
backslashes, links, special files, duplicate paths, file/directory conflicts and
sibling filenames equal except for their final extension are rejected. The
filename comparison uses Unicode NFC and case folding, with leading-dot-only
names handled like FileBeans. No dataset exception is applicable to this media
archive import. Archive permissions/ownership are not restored; imported files
use 0644 and directories 0755.

The maximum is 128 GiB expanded content, 100,000 distinct paths, 64 path
components and 4096 bytes per path. Decompressed tar metadata/padding has an
additional 256 MiB bound. Available destination space must cover content plus
1% encryption overhead and a 1 GiB reserve. No plaintext media staging occurs
outside the encrypted destination. These are intentionally conservative import
limits; a rejected larger archive requires a reviewed change before execution.

The Go race suite covers valid nested paths, Unicode/case/extension collisions,
duplicate entries, traversal, links, truncated gzip, recovery checksum failure,
existing destination preservation, roundtrip hashes and no-overwrite rename.
The on-Pi disposable FUSE test covers two vaults, raw master-key stdin, encrypted
tar extraction, long names, subfolders, SHA-256 readback, RENAME_NOREPLACE and a
read-only source remount. Test fixtures are removed on exit. It never uses real
protected folders.

## Rebuild

Build on the VM inside development, using bounded disk-backed scratch for
`TMPDIR`, `GOCACHE`, `GOMODCACHE`, and outputs. From this directory:

```sh
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o worker .
```

Setup used checksum-verified official Go 1.27.4. gocryptfs was verified with
upstream signing fingerprint `FFF3E01444FED7C316A3545A895F5BC123A02740`;
ARM64 executable SHA-256:
`8d254ef44ecd2c0cfa027d332f519bb64b0450437816dc4368c4a28618bacdfc`.
Scripts use the upstream s6 `S6_STAGE2_HOOK` and a prebuilt HA base image so no
ARM64 image builder is needed. Transfer binaries to `.next`, verify checksums,
stop only this app, then atomically replace and restart. Repeat the synthetic
selftest after relevant changes.

Setup verification: final worker SHA-256
`59ccf7c8567d46d53d9007b9014cabf17e81d08e2a13ea6c45fb14edb26fb8fb`.
The final Pi FUSE test passed and verified all disposable fixtures absent;
HA Core reported 0.88% CPU immediately afterward. The helper remains installed
and idle pending the user's hidden terminal entry and archive inspection.

VM preparation correction: the development container did not inherit the host's
intended 2 GiB scratch mount. The 653 MiB preparation tree was on underlying
ext4 and was removed along with the unused mount/backing file. Future preparation
must verify the bounded mount with `findmnt` **inside development** before any
downloads or build work; a host-only check is insufficient.

A user-supplied key file can be consumed directly on Pi without terminal entry:
`bash /local_apps/filebeans_extract/key-file.sh JOB.json KEY_FILE`.
Only the filename is in argv; contents stay in local process pipes. The original
credential file is preserved. Both entry paths normalize recovery-code letter
case before the worker validates the checksum. Do not run a second request while
an inspection/extraction is active; the worker serves requests serially.

## Layout correction and listing issue — 2026-11-25

The tar contained an outer `pics archive` directory. After the user reported the
unwanted wrapper, its 49 immediate entries were promoted directly into Samba
`/pics` using same-filesystem, no-overwrite renames on Pi. The original wrapper
was removed only after emptying it. Counts before/after remained 743 files and
45,032,055,965 bytes; nested folders were preserved. No media crossed the network.
The original 44,861,578,484-byte tar.gz remains in Pic archive. No global lock or
VPS protection change was made.

The short-lived repair script ran inside the existing constrained helper using
the original Pi-local key pipe; it and all maintenance mountpoints were removed
and the helper was stopped. The helper's normal selftest was restored. Its
installed package remains pending the final FileBeans viewing check and removal.

Read-only production diagnostics found direct directory enumeration took 1.174
seconds, while the corrected folder's API listing took 48.41 seconds for 49
entries. Listing repeats complete protected-boundary classification per child
and per ancestor. A focused API fix pins the already-validated protected
directory for child Lstat calls while retaining the request's protection lease;
ordinary directory checks are unchanged, symlinks remain excluded, and canceled
requests stop enumerating. Filesystem race tests passed, including directory
replacement/symlink confinement. Deployment and live timing verification remain
pending an explicit server deployment request; no client build is required.

The live archive-link listing was empty during diagnosis. Phase two must resolve
or establish the intended link before relying on archive thumbnail suppression;
no link was created as part of this layout repair.
