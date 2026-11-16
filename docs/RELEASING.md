# Signing and TestFlight

The public app identity is recorded in `Config/Release.json` and `Config/Project.xcconfig`:

| Setting | Value |
| --- | --- |
| App display name | fileBeans |
| App Store Connect record | FileBeans (`6835142643`) |
| Main bundle ID | `com.agawlbb.myfiles` |
| Files extension | `com.agawlbb.myfiles.FileProvider` |
| Shared App Group | `group.com.agawlbb.myfiles` |
| Shared Keychain group | `FKR675M7PM.com.agawlbb.myfiles` |
| App Store SKU | `myfiles-ios` |
| Primary locale | English (U.S.) |

## Current setup

Both bundle IDs and their shared App Group are registered and linked. The GitHub `testflight` environment permits releases from the branch selected when dispatching the workflow. Its Apple team/bundle variables, five API/P12 credential secrets, and both provisioning-profile secrets are configured. The reusable distribution identity expires November 15, 2027; its public certificate ID is recorded in release configuration.

The encrypted private key, P12, password and reuse helper are permanently retained in the VM's private Apple store, accessible read-only inside development. The installed `apple-signing inspect` command verifies them. **Do not delete the retained files after configuring GitHub or create a certificate for each project.** Future projects should use the shared workspace signing-reuse instructions. This repository contains no private signing material.

The App Store Connect iOS record now exists as **FileBeans**, with the registered bundle ID and SKU above. Apple rejected the original myFiles name; the device app and Files extension display **fileBeans**. The repository and VM checkout are named fileBeans. Xcode project/target names, bundle IDs, App Group and SKU retain their existing identifiers.

The two App Store profiles were generated and validated against the registered App Group, shared Keychain access, and retained distribution certificate. Apple registration and signing setup are complete.

To inspect the setup or refresh app-specific profiles, run the helper from the VM:

```bash
dev node /home/dev/projects/fileBeans/scripts/apple-setup.mjs inspect
dev node /home/dev/projects/fileBeans/scripts/apple-setup.mjs profiles
```

The second command reuses the existing distribution certificate, creates the two app-specific App Store profiles, verifies App Group/Keychain/certificate/expiry constraints, and stores them directly as `APP_PROFILE_BASE64` and `PROVIDER_PROFILE_BASE64` in GitHub. It does not issue certificates, build apps, upload packages, invite testers or change beta groups.

## Environment settings

| Variable | Purpose |
| --- | --- |
| `APPLE_TEAM_ID` | Registered Apple team |
| `BUNDLE_ID_PREFIX` | Main bundle identifier |
| `APPLE_DISTRIBUTION_CERTIFICATE_ID` | Reusable distribution certificate metadata |

| Secret | Purpose |
| --- | --- |
| `DISTRIBUTION_P12_BASE64` | Encrypted distribution certificate/private-key package |
| `DISTRIBUTION_P12_PASSWORD` | Package password |
| `ASC_PRIVATE_KEY_BASE64` | Existing App Store Connect API key |
| `ASC_KEY_ID`, `ASC_ISSUER_ID` | API authentication metadata |
| `APP_PROFILE_BASE64`, `PROVIDER_PROFILE_BASE64` | Main app and extension App Store profiles |

When refreshing credentials for this project, the shared VM helper installs the retained identity directly into the `testflight` environment. There is no workflow-based extraction from other repositories and no credential artifact transfer. Temporary copies on ephemeral GitHub runners are removed after use; the VM originals remain intact.

## Current release — November 16, 2026

**0.1.0 (13)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `f5120ad25c36026f692a5b264d2fe1f16fa132c8`, immutable tag
  `testflight/0.1.0-13`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43946193403):
  simulator tests and unsigned iPhone/iPad Release archiving passed. Both source
  navigation lifecycle tests and all three breadcrumb tests passed.
- Downloaded test-result and unsigned-archive checksums matched GitHub's SHA-256
  digests. Packaged app/extension identifiers, version 0.1.0, iOS 18 minimum,
  and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43949614169):
  signing preflight, archive/export, TestFlight upload and credential cleanup passed.
- Apple build ID: `f200a7a6-7d86-4e41-88d6-4175fbca43d9`.
- Compact source navigation uses a stable stack. Folder titles show tappable
  ancestor breadcrumbs, abbreviating to a source and hierarchy menu when space
  is limited. The iPad sidebar also displays the current folder hierarchy.
- Physical-device layout, live-source switching, and iPad sidebar acceptance
  remain manual. No VPS deployment is needed for this app-only release.

## Previous release — 0.1.0 (12)

**0.1.0 (12)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `de85cab006f2a7c2a13a4b6dd962f6daaf5833b3`, immutable tag
  `testflight/0.1.0-12`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43935825811):
  simulator tests and the unsigned iPhone/iPad Release archive passed.
- Downloaded test-result and unsigned-archive SHA-256 checksums matched GitHub's
  artifact digests. Packaged app/extension identifiers, version 0.1.0,
  iOS 18 minimum, and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43939202252):
  preflight, archive/export, TestFlight upload and temporary credential cleanup
  passed on the Intel macOS runner.
- Apple build ID: `30dec7f5-5834-4d86-a550-79fa6348edc2`.
- Each source opening creates a fresh detail view/task lifetime. Returning to the
  compact source list clears selection, cancelled listings do not present errors,
  and Drive waits for a successful status check before offering sign-in.
- Physical iPhone/iPad back/reopen and source-switching acceptance remains manual.
  This release changes only the app; no VPS deployment is needed.

## Previous release — 0.1.0 (11)

**0.1.0 (11)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds. No tester/group changes
or App Store submission were made.

- Source: `f8957caf4189cf920f3744e8c7d6732071132487`, immutable tag
  `testflight/0.1.0-11`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43929950557):
  all **62** simulator tests passed, including source aggregation and persistent
  thumbnail caching; the unsigned iPhone/iPad Release archive passed.
- The downloaded test-result and unsigned-archive artifact checksums matched
  GitHub's SHA-256 digests. Packaged app/extension identifiers, version 0.1.0,
  iOS 18 minimum, and iPhone/iPad support were verified.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43932404308):
  preflight, archive/export, TestFlight upload and temporary credential cleanup
  passed on the supported Intel macOS runner.
- Apple build ID: `cf1fb6c3-67e7-49e1-9afc-0e000507656c`.
- The home page now lists all connections' sources, with connection management
  in Settings and connection subtitles when multiple connections exist.
  Thumbnails persist locally with a 200 MB disk limit. Videos use native AVKit
  controls for full screen playback while preserving the player and stream.
- Physical iPhone/iPad navigation and full screen transition checks remain manual
  acceptance. This release changes only the app; no VPS deployment was needed.

## Previous release — 0.1.0 (10)

**0.1.0 (10)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access. No tester/group changes or App Store
submission were made.

- Source: `7ebd7035526d04f4ba48ebf670a80c6027ba0e78`, immutable tag
  `testflight/0.1.0-10`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43912220792):
  50 simulator tests passed, including HEVC Main10 native playback/seek and
  normalization controls; the unsigned iPhone/iPad Release archive passed.
- [Signed release](https://github.com/agawlbb/fileBeans/actions/runs/43916695794):
  signing preflight, archive/export, upload and temporary credential cleanup passed.
- Apple build ID: `77e5818b-ca6a-4e0a-ae39-0ea73622b0d0`.
- The companion and separate FFmpeg worker run the same source on the VPS.
  **Transfers → Media Normalization** exposes job/batch progress, cancel and retry.
  Drive imports download, convert, validate, publish and clean one file at a time.
  New stable VPS `/pics` media is normalized automatically; existing VPS Trash
  and backup policies continue. This feature does not mount or modify Samba.
- The photo pilot completed. The HD video pilot is converting under a separate
  1536 MiB/one-CPU worker limit. A bounded one-time server gate will queue the
  initial Drive library only after both pilots complete and publish; failures
  leave the full import stopped. Full population is background work, not complete
  at release time. Physical iPad quality comparison remains manual.

## Previous release — 0.1.0 (9)

**0.1.0 (9)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds; no tester/group changes
or App Store submission were made.

- Release source: `7772ca4`, immutable tag `testflight/0.1.0-9`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43877396959):
  all 46 simulator tests passed, including Drive permission/empty-page/sort tests,
  and the unsigned iPhone/iPad Release archive passed. Validated source `a95aa5a`
  has identical App/Shared/Tests/FileProvider/project/workflow inputs to the release;
  the subsequent change affects only server account-reconnection recovery/docs.
- [Signed TestFlight upload](https://github.com/agawlbb/fileBeans/actions/runs/43880475777):
  signing preflight, archive/export, upload, and runner credential cleanup passed.
- Apple build ID: `ac2ec1c2-7883-42a9-8634-378ecf5aa991`.
- The VPS companion runs source `7772ca4`. Go race/MariaDB recovery tests and live
  copy/trash/Drive-browsing smoke checks passed; live Drive writes require fresh
  full-access consent and remain part of device acceptance.

After updating, open the connection's **Google Drive → Upgrade Drive Access** and
approve full Drive permission. A disconnected account instead shows **Connect
Drive**. MakeLORA's separate token is unchanged. Google Drive uses the same
list/grid browser and supports native file management, server-side copies,
Files/Photos uploads, thumbnails, media playback, and native trash. Google-native
documents open in Google's browser; server export/conversion remains deferred.
Drive stays outside Apple Files. Trash shortcuts are separate bottom bars only
at each source's top level, with no combined Trash source.

For an unconfigured connection, set companion URL
`https://files.coffeybean.com/companion` and its separate token from the private
VPS connection file or the existing `vps_exchange` copy. Never commit or log it.
The existing OAuth client and registered callback remain configured. Physical
device acceptance, real Google write operations and codec compatibility remain
manual checks after sign-in; simulator success does not claim those checks.

### Previous release

**0.1.0 (8)** was `VALID` / `IN_BETA_TESTING`, Apple build ID
`947e98db-3f9c-4436-a491-3e1fe6e45dd6`, source `ab60f7d`, tag
`testflight/0.1.0-8`. Its [validation](https://github.com/agawlbb/fileBeans/actions/runs/43853796671)
passed 40 tests and device archiving; its [upload](https://github.com/agawlbb/fileBeans/actions/runs/43858523541)
succeeded. One stale branch-based request
[`43856976402`](https://github.com/agawlbb/fileBeans/actions/runs/43856976402)
eventually ran and failed preflight because build 8 already existed. Signing and
upload were skipped; it did not produce a second release. Do not rerun it.

## Manual workflow

Initial build verification on November 15, 2026: source `e335203` passed all 19 tests and produced an unsigned device Release archive for version **0.1.0 (1)**. The [successful run](https://github.com/agawlbb/fileBeans/actions/runs/43734190564) includes the archive artifact. Its downloaded artifact digest matched GitHub, and both bundle identifiers, the embedded Files extension, iOS 18 minimum, and iPhone/iPad support were verified. The archive SHA-256 is `b2fd58c41e00a24c64a61c4358b58b91b1c8853814f9735c497748d2fcfd5876`. It has no distribution signature and has not been uploaded to TestFlight.

The TestFlight workflow has three explicit operations:

- **preflight** (default): Ubuntu-only credential, profile, App Group, Keychain, app-record and certificate checks. No build or upload.
- **build**: after preflight passes, create a signed archive/IPA on GitHub macOS and retain the IPA for seven days. No upload.
- **upload**: create the signed package and upload it to TestFlight. Requires release authorization; existing beta-group settings may distribute the uploaded build.

Select the source branch in the workflow’s **Run workflow** menu for any operation; merging into `main` is not required.

A custom positive build number is optional; otherwise the workflow run number is used. An already-uploaded number is rejected. Run Validate iOS and the [device acceptance checks](TESTING.md) before an authorized release. The workflow does not configure testers, submit an App Store release, or change tester groups.

The workflows use Xcode 26.3 on `macos-15`, meeting Apple's [Xcode 26 / iOS 26 SDK upload requirement](https://developer.apple.com/news/upcoming-requirements/?id=04282026a), while targeting iOS 18+. Physical-device acceptance remains manual. Verify privacy answers, the privacy manifest and encryption declaration against the final shipped behavior before release.

Both workflows accept a macOS runner choice. The default remains `macos-15`
(Apple Silicon). If that hosted pool stays queued, `macos-15-intel` uses the same
macOS/Xcode 26.3 toolchain on Intel; simulator tests still precede release.

Validation selects an available iPhone runtime matching the selected Xcode SDK
(or its newest compatible installed runtime). Simulator boot has a separate
ten-minute limit; compilation/tests have twenty minutes. This keeps fresh Intel
runner setup from consuming the entire test execution budget.
