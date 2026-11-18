# Protected-folder Quantum consumer

This directory contains an adapter and checked source transformations for
FileBrowser Quantum `v2.0.2-stable`, upstream revision
`2c071d1e6eb48d4b88da2657885d345663a0a88b`. Upstream source and its license remain
in the prepared dependency; this repository does not vendor a second checkout.

Run source preparation in VM-laptop development:

```sh
dev bash -lc 'cd /home/dev/projects/fileBeans && python3 scripts/prepare-quantum.py'
```

The codeload archive SHA-256 is
`f9f783bda505d47ff86b290519338c8b4f28069bb2a29f8e35ad56bc66de7a1e`.
Preparation rejects checksum/anchor changes and replaces only its marked
dependency directory under ignored `.build/quantum/source`. It adds a local
module reference to `companion/protection`; no key implementation is copied into
Quantum. Rebase the transformations and rerun acceptance tests before upgrading.

`FILEBEANS_VAULT_RUNTIME` enables the consumer. `FILEBEANS_VAULT_SOURCES` maps
the companion's source names to matching absolute source directories as JSON.
Both variables must match the host broker. The consumer runs as the configured
unprivileged reader UID and shared primary GID. Startup validates group creation
permissions, sets umask `0007`, and starts its five-second capability heartbeat.
Missing/incompatible broker state fails closed. Without these variables the
adapter leaves ordinary upstream behavior enabled.

The transformations install whole-request operation leases and guard filesystem
reads/mutations before cache hits. Protected previews, FFmpeg scratch files and
archive spools use encrypted private storage. Protected files are excluded from
the persistent SQL index. Filename-bearing user pins/scopes, sharing and access
rules are checked before persistence; filesystem activity and application/API
logs are suppressed in this mode. Protected OnlyOffice conversion is disabled.
Archive selections cannot span multiple vaults; resume tokens bind their
selection and principal. Proxy buffering/log settings remain administrator setup.

`Dockerfile` builds the patched backend with the upstream native PDF tags and retains matching frontend/FFmpeg/runtime assets
from immutable image digest
`sha256:de2b4c4d18485ea202ab2cdada5bd25d8f0b8ed071712c8216a67194c31285ae`.
Its Dockerfile-specific ignore file admits only prepared backend and companion
sources. Native PDF and text-to-JPEG tests execute before the backend build.
Image builds require the user's request and VM image-builder procedure.

Run the relevant upstream race suites from the prepared backend, using the
installed Go tool, and run `sh scripts/test-vault-fuse.sh` for the isolated
real-filesystem integration suite. That script prepares test executables through
`dev`; it does not build an image. Install `scripts/requirements-smb-tests.txt`
in the project `.venv` for the loopback SMB fixture. Required tools are gocryptfs
2.6.1, FUSE 3, FFmpeg/ffprobe and the existing VM development runtime image.
The suite includes independent ciphertext recovery and does not use real keys.

See [runtime setup](../docs/VAULT-BROKER.md) and
[behavior and acceptance limits](../docs/PROTECTED-FOLDERS.md). Quantum image
compilation, native PDF/text rendering and isolated image execution passed on November 18, 2026. No deployment or release has occurred.
Apple workflow results are recorded in the protected-folder documentation.
