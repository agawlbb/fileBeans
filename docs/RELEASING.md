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

## Initial build-only verification — 0.1.0 (14)

Source `b7ac1b93c30dfac816fef19e65b33c0a0144f2f6` builds the corrected single large
breadcrumb heading in place of the folder title.

- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43952665641):
  simulator tests and unsigned iPhone/iPad Release archive passed.
- [Signed build](https://github.com/agawlbb/fileBeans/actions/runs/43956876063):
  preflight, signing, archive/export, artifact retention, and credential cleanup
  passed. The TestFlight upload step was skipped (`operation=build`).
- Signed IPA artifact: `myFiles-signed-14`, retained for seven days at
  [artifact 14271014162](https://github.com/agawlbb/fileBeans/actions/runs/43956876063/artifacts/14271014162).
- Downloaded test-result, unsigned-archive, and signed-IPA artifact SHA-256 digests
  matched GitHub metadata. Both packaged targets have version 0.1.0 (14), correct
  bundle IDs, iOS 18 minimum, and iPhone/iPad support.
- This initial build skipped upload; subsequent explicitly authorized publication
  is recorded below. No VPS deployment is needed.

## Feature-branch rollout — November 18, 2026

**0.1.0 (18)** is signed, uploaded and available to the existing internal
TestFlight group. Apple reports **VALID** / **IN_BETA_TESTING**. Source remains
on `feature/encrypted-vaults`; `main` has not been merged or changed.

- Release source: `9358f30fe8d9c3a164d2f7ba07068f0565c3ce5b`, immutable tag
  `testflight/0.1.0-18`.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/44215546871)
  passed preflight, signing, archive/export, TestFlight upload and temporary
  runner credential cleanup. Apple build ID:
  `238dd3dc-e172-427a-9fe0-81849ce27c98`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/44213282822)
  passed all 83 simulator tests and the unsigned iPhone/iPad Release archive.
  Application inputs match the released source; subsequent deployment changes
  affect server code and documentation only.
- The compatible Quantum, companion, normalizer and host mount broker are now
  deployed. Authenticated HTTPS advertises `protectedFoldersCreate`; the app
  can create protected folders from the Add menu. Existing source names/paths,
  accounts, OAuth grants and queued normalization work were preserved.
- The deployment found a separate-UID normalization directory issue. Server
  source `74893ba14a83f468cae31247a12b00364eb63c03` accepts an already group-writable
  directory when an ownership-only chmod is denied. Companion race/MariaDB and
  real local/CIFS/FUSE tests pass. This server-only fix does not require another
  app build.
- No existing files were converted, no vault keys were generated on the server,
  and no production vaults were created during deployment. Physical-device key
  backup/import, biometrics, backgrounding and Files acceptance remain manual.
- No tester groups, certificates, credentials or repository protections changed.
  See [the deployed broker/runtime record](VAULT-BROKER.md#deployed-runtime--november-18-2026)
  for image digests, identities, mounts, rollback material and live checks.

## Feature-branch rollout — November 17, 2026

**0.1.0 (17)** has been signed and uploaded successfully. Apple reports
**VALID**; internal TestFlight distribution is still **PROCESSING** at the last
check. Source remains on `feature/encrypted-vaults`, unmerged into `main`.

- Companion source `9f342269f2a667300685dc251481a5f307ca6c25` is deployed as
  `filebeans-companion:9f34226`. Its archive checksum and relevant live checks
  passed; the normalizer was not recreated. See the companion deployment record.
  No vaults, storage migration or existing-file encryption were configured.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/44136905919)
  passed simulator tests and unsigned iPhone/iPad Release archiving. The downloaded
  unsigned archive and simulator result ZIPs matched GitHub SHA-256 digests
  `b2a4ea4cadd2cfa8e89203bc4d4e1232047d7f7d21d2b5d5db453436cc72a03f`
  and `660a6a42a9332e69adc57b02ba29c8a3c165841d2129c5a1df02a072f6dcc609`.
  Both packaged targets have the expected identifiers, version 0.1.0, iOS 18
  minimum and iPhone/iPad support. The unsigned archive uses default build 1;
  the signed release overrides that with build 17.
- Release source `df87e87846a76e9bc202628028c0e121015a42b2`, immutable tag
  `testflight/0.1.0-17`, adds signing checks
  for the main app's private `.vaults` Keychain group; all six release-helper
  tests pass. Its application/test/project/workflow inputs match validated
  source `9f34226`.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/44141007306)
  succeeded on retry directly from the feature branch: preflight, signed
  archive/export, TestFlight upload, and temporary runner credential cleanup
  passed. Apple build ID: `c7fe1cff-be36-47a1-8fc7-1ad825ba259b`.
  Apple recorded the upload on November 17 at 19:53:52 UTC.
- The [initial attempt](https://github.com/agawlbb/fileBeans/actions/runs/44141007306/attempts/1)
  failed during Apple's outage with HTTP 500 before signing began. On the later
  retry an intermittent `/v1/apps` HTTP 500 cleared, and the next attempt passed
  all original signing checks; none were bypassed.
- No tester groups, credentials, certificates or repository protections were
  changed. The existing internal group still has access to all builds and will
  receive this build after Apple's processing. Device acceptance for biometric key storage/import/export and vault
  controls remains manual. The server has no vaults configured yet.

## Previous published release — November 16, 2026

**0.1.0 (16)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal beta group retains access to all builds.

- Source: `84ecad127bcb62d2cc1ab18d27076a3f105c329d`, immutable tag
  `testflight/0.1.0-16`. All outstanding implementation work was merged to `main`
  before deployment and release.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43976688057):
  all **68** simulator tests and unsigned iPhone/iPad Release archiving passed,
  including queued-transfer filename/destination decoding.
- Both downloaded validation artifacts matched GitHub SHA-256 digests. Packaged
  app/extension identifiers, version 0.1.0, iOS 18 minimum, and iPhone/iPad support
  were verified. The signed workflow overrides the validation archive's default
  build number with build 16.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43978748344):
  signing preflight, signed archive/export, TestFlight upload, and temporary runner
  credential cleanup passed.
- Apple build ID: `e0be188e-e5cc-4be6-af00-3b63598cbca3`.
- Transfer controls show cancellation progress immediately, display submitted
  source/destination and filenames for new Drive jobs, and hide completed copies.
  Existing earlier submissions retain their available path/ID fallback.
- The companion was deployed first as `filebeans-companion:84ecad1`. It adds
  bounded parallel copying/discovery, directory-handle reuse, batched recovery
  updates, and fewer redundant flushes and filesystem operations. Go race/MariaDB
  tests, Go vet, and live storage/Drive smoke checks passed. A live 32-file Samba
  probe improved from 30.757 to 8.729 seconds (3.52x); this does not measure Drive
  bandwidth or isolate individual optimizations.
- Only the companion was recreated. The video normalizer's container and
  `2026-11-16T17:10:17.690121031Z` start time remained unchanged; its conversion
  continued progressing. No schema migration was required.
- Physical-device transfer controls and layout acceptance remain manual.

## Previous release — 0.1.0 (15)

**0.1.0 (15)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `952950a8128d059821907bf65b97c93569a73e4f`, immutable tag
  `testflight/0.1.0-15`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43960236333):
  simulator tests and unsigned iPhone/iPad Release archiving passed.
- Both downloaded validation artifacts matched GitHub SHA-256 digests. Packaged
  app/extension identifiers, version 0.1.0, iOS 18 minimum, and iPhone/iPad support
  were verified.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43964841908):
  preflight, signed archive/export, upload, and temporary credential cleanup passed.
- Apple build ID: `2eda1c7b-19bb-4194-9541-e6cdd548f07f`.
- The heading uses smaller path text and keeps Sources directly visible and
  tappable at every width. While browsing on iPad, the sidebar shows only
  Sources → selected source → folders; tapping Sources returns to the picker.
- Physical-device layout acceptance remains manual. No VPS deployment is needed.

## Previous release — 0.1.0 (14)

**0.1.0 (14)** is uploaded; Apple reports **VALID** / **IN_BETA_TESTING**.
The existing internal group retains access to all builds.

- Source: `b7ac1b93c30dfac816fef19e65b33c0a0144f2f6`, immutable tag
  `testflight/0.1.0-14`.
- [Validation](https://github.com/agawlbb/fileBeans/actions/runs/43952665641):
  simulator tests and unsigned iPhone/iPad Release archiving passed.
- Initial signed IPA build and validation artifacts were downloaded and verified
  against GitHub SHA-256 digests. Both targets have the expected identifiers,
  version 0.1.0 (14), iOS 18 minimum, and iPhone/iPad support.
- [Publication](https://github.com/agawlbb/fileBeans/actions/runs/43957926938):
  preflight, signed archive/export, TestFlight upload, and credential cleanup passed.
- Apple build ID: `5e839cb5-480c-4c26-b50f-2916caadd20a`.
- The folder hierarchy replaces the original folder-title heading in one large
  row below the navigation controls. Narrow layouts abbreviate the hierarchy
  while retaining source access and a menu of ancestors. The iPad sidebar retains
  its active hierarchy.
- Physical-device layout acceptance remains manual. No VPS deployment is needed.

## Previous release — 0.1.0 (13)

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
