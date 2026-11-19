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

## File-management updates — 0.1.0 (20)

**0.1.0 (20)** is **VALID / IN_BETA_TESTING** for the existing internal
TestFlight group. Apple build ID:
`2c442a28-9e9f-419e-8b37-8920fab5bb6b`.

- Release source: `708525a25b91f2e77be3798abe6c2bbe53a7d829`, tag
  `testflight/0.1.0-20`, on `feature/encrypted-vaults`.
- [Final validation](https://github.com/agawlbb/fileBeans/actions/runs/44330888062)
  passed the simulator suite, including all eight file-operation-state tests
  and the batch-submission regression, followed by the unsigned device archive.
- [Signed publication](https://github.com/agawlbb/fileBeans/actions/runs/44336852425)
  passed signing, export, upload and temporary credential cleanup. The first
  publication dispatch 44335959946 was cancelled while waiting for a runner,
  before signing or upload.
- The app provides one flat Trash view per source, hides locked protected
  folders and their Trash entries, exposes Select/Select All for batch file
  operations, shows pending destination tiles and immediate deletion progress,
  and lists existing files in the destination picker.
- Source `05bcd79` is deployed in the companion and `f334c3a` in Quantum.
  Locked-root listing and private Trash behavior passed real local/CIFS/FUSE
  tests. Live HTTPS verified all six file/folder Trash lifecycle combinations
  across VPS, ordinary Samba and protected Samba, plus three-file copy/move
  jobs with matching contents and original retention/removal.
- The existing retained protected item migrated without a fingerprint change;
  the old public per-folder Trash directory was removed. Encrypted rollback
  metadata is in the originating vault's private area. The normalizer container
  and key-holding broker were preserved, with the existing vault unlocked.
- No schema migration, replacement encryption key, tester-group change or
  signing-identity change was required. The feature remains unmerged into main.

## Protected-folder moves and merged normalizer — November 18, 2026

**0.1.0 (19)** is available to the existing internal TestFlight group:
**VALID / IN_BETA_TESTING**, Apple build
`79db23a4-ac3b-4472-bbeb-c72e053aeec5`.

- Release source `87ae6efc9ac783a22c4c7046a189f81230898640`, tag
  `testflight/0.1.0-19`, on `feature/encrypted-vaults`.
- [Validation 44301022097](https://github.com/agawlbb/fileBeans/actions/runs/44301022097)
  passed all **87 simulator tests** and the unsigned iPhone/iPad Release archive.
  It validated `cb9fb2b`; application, extension, tests, configuration and workflow
  inputs are identical at the merged release source.
- [Upload 44305782407](https://github.com/agawlbb/fileBeans/actions/runs/44305782407)
  passed signing, export and upload. A redundant
  [dispatch 44305782427](https://github.com/agawlbb/fileBeans/actions/runs/44305782427)
  exported the same version but Apple rejected its upload as duplicate build 19.
  Its green workflow status does not establish a second upload. App Store Connect
  confirms the single available build above.
- Initial validation attempt 44298859957 passed the new move tests but timed out
  in the existing media seek test. Its queued retry was cancelled before the
  successful Intel validation above.
- `origin/main` commit `258bc7e` was merged into the feature branch as `87ae6ef`.
  The two normalizer merge conflicts retain guarded filesystem access alongside
  isolated pipeline workspaces. The feature branch has **not** been merged into
  `main`.
- VPS companion and normalizer run `87ae6ef-vaults` images; Quantum runs
  `cb9fb2b-vaults` with unchanged Quantum/protection inputs after the merge.
  The normalizer has a 4 GiB memory/swap limit, no CPU quota, and CPU shares 128.
  Quantum and broker were preserved during the normalizer upgrade; the broker
  retained PID 1637955 throughout both deployments and its vault stayed unlocked.
- Go race/MariaDB tests pass, with only the established unrelated staged-hardlink
  baseline excluded; Go vet, source checks and patched Quantum race tests pass.
  Real local FUSE/CIFS tests cover boundary-conflict responses, moves in both
  directions, durability and shared-user writes. Journal tests cover publication,
  quarantine, partial deletion, cancellation and source/destination changes.
- Authenticated live HTTPS moved disposable data into and out of the unlocked
  vault. Both moves verified content/empty directories and removed the original;
  the probe was cleaned up. No new production key or vault was created.
- All 743 normalization jobs remained accounted for. Four more completed after
  the merged rollout; three jobs were in flight at the final check. The interrupted
  encode hit the existing restart safeguard and was requeued through the supported
  retry API after the resource upgrade. Existing completed outputs were retained.
- Prepared-image checksums and rollback compose files are retained under
  `/opt/services/filebeans/deploy/moves-cb9fb2b` and
  `/opt/services/filebeans/deploy/normalizer-87ae6ef`. No schema migration,
  tester-group change or signing-identity change was needed.

## Earlier feature-branch rollout — build 18

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
VPS connection file or the existing Samba `fileBeans/vps_exchange` copy. Never commit or log it.
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

The workflows select stable Xcode 27.2 on GitHub's `xcode-27` Apple Silicon
runner ([installed toolchains](https://github.com/actions/runner-images/blob/main/images/macos/xcode-27-arm64-Readme.md)).
Both workflows verify that the selected device and simulator SDKs are version 27
or later before compiling. The deployment target remains iOS 18+. Physical-device acceptance remains manual. Verify privacy answers, the privacy manifest and encryption declaration against the final shipped behavior before release.

Both workflows accept the `xcode-27` and `xcode-27-xlarge` Apple Silicon runner
pools. Both select Xcode 27.2 explicitly; simulator tests precede release.

Validation selects an available iPhone runtime matching the selected Xcode SDK
(or its newest compatible installed runtime). Simulator boot has a separate
ten-minute limit; compilation/tests have twenty minutes. This keeps fresh Intel
runner setup from consuming the entire test execution budget.

### Protected-folder inactivity deployment (2026-11-18)

Broker source `70346d4362353a4998442ddca72f0d8278c753a4` is deployed with a
fixed one-hour inactivity timer. VM-built Linux/amd64 broker SHA-256:
`dc51ce708fdd9298b9580d41f52b9c8778549d9138c44f932a65adf3ebc33534`.
The running executable hash and startup policy message were verified, and the
public companion vault/trash APIs responded successfully. The deployment safely
locked the existing protected folder before restarting the broker; unlock it
from the current app. RuntimeDirectoryPreserve=restart retains the runtime
directory used by consumer binds. The existing protected-stack systemd dependency
also restarted the consumers with their existing images: companion 05bcd79,
Quantum f334c3a, normalizer 87ae6ef. No app build or release was made.

Validation: companion race suite (the documented unrelated staged-hardlink
baseline remains excluded), Go vet, and the isolated FUSE suite passed. New local
and Samba-backed idle tests advance the clock through the exact one-hour boundary,
verify reads extend the timer, exclude directory polling/private cache writes,
defer while a client holds a lease, retry successfully, and confirm recovered
bytes after unlocking. The production hour was not shortened for testing.


### Parallel normalizer deployment (2026-11-18)

Normalizer source `fed272d19f2bac77957172b43163ac2f36041aaa` was built on
VM-laptop and deployed as `filebeans-normalizer:fed272d-parallel`. Current main
contains the deployed encrypted-vault integration; no feature checkout or app
build was needed. Only the normalizer image reference changed in production
Compose, and only that service was recreated with `--no-build --no-deps`.

- Two simultaneous HEVC encoders were observed, both using two frame threads,
  medium/CRF 18 and Main 10. Separate progress readings confirmed advancement;
  one observed video completed full validation/publication during verification.
- The worker retains a 4 GiB memory limit, 4 GiB memory-plus-swap limit (no swap),
  uncapped CPU and CPU shares 128. Early live readings showed about 0.55–0.70 GiB
  memory use and 313–379% CPU, with zero restarts or OOM termination. The existing
  3.25/3.5 GiB admission/fallback thresholds remain unchanged.
- All 743 job IDs were retained. All 449 pre-deployment completed records matched
  exactly, and their 449 output files retained size, modification time and inode.
  Three interrupted jobs were cancelled/retried through supported API controls,
  retaining their IDs, so startup recovery did not serialize the new pipeline.
  Final observed totals: 451 completed, four admitted/running, 288 queued.
- Companion and Quantum container IDs/start times and vault broker PID 2104738
  were unchanged. All unrelated containers initially matched the preflight
  snapshot; flightdata-service-1 separately restarted later during verification.
  The deployment commands targeted only the normalizer.
- Focused normalization workflow and resource/completion checks passed with the
  Go race detector before packaging. The deployed binary SHA-256 is
  `b061b446710f75bb9dd0cb6622bfe2386e252668792b8646d6716cb555ecf282`.
  Archive SHA-256:
  `c709162296d780d0a4ca732d1cf7371e5b0c60de945d54dceb4edac17d473d51`.
  The VPS containerd image identity is the verified OCI manifest digest
  `sha256:d034d3267539f0f88a0b5feaceac356f3f684fdfed7e870bb0c18ef6fcabfb32`.

VM artifacts remain under `.build/normalizer/fed272d19f2bac77957172b43163ac2f36041aaa/`.
The transferred archive, metadata, original Compose file, queue/output snapshots
and verification reports are retained privately under
`/opt/services/filebeans/deploy/normalizer-fed272d/`. For an authorized rollback,
change only the normalizer image to `filebeans-normalizer:87ae6ef-vaults` and
recreate only that service with `--no-build --no-deps`; preserve any later changes
to other services. No schema migration, completed-file re-encoding, broker
restart, app build or release was performed.

### Folder operations, recovery import and SDK 27 release (2026-11-18)

- App source: `8dd77d70c794aa8e659333333a3868c22b188f2d`, immutable tag
  `testflight/0.1.0-21`. A separate normalization commit subsequently landed on
  main; the app release remains pinned to this validated source.
- Signed upload: [44369352108](https://github.com/agawlbb/fileBeans/actions/runs/44369352108),
  build **0.1.0 (21)**, Apple build
  `638eeb43-78cc-4fa7-99d0-aac83139fabb`, **VALID / IN_BETA_TESTING**.
  The log reports device and simulator SDK **27.2** and upload success with no errors.
- [Apple validation 44366675017](https://github.com/agawlbb/fileBeans/actions/runs/44366675017)
  passed its simulator tests and unsigned iPhone/iPad archive steps on the same source.
  Device biometrics and transfer of an actual user key between physical devices
  remain manual acceptance; synthetic recovery-code formatting and session-import
  regressions are included in the simulator suite.
- The VPS runs companion `filebeans-companion:8dd77d7-folders` and the matching
  broker. VM image archive SHA-256:
  `7f17e27c5723096e85d658035383e3ee9737c201ed93eedb63ec7cecc3075361`.
  Manifest `sha256:03153e3eace206ade29a209342998b1a1e52d0ea944aa5fd325d6cefdf091338`.
  Broker executable hash was matched to the VM runtime manifest.
- Deployment backups and image metadata are under
  `/opt/services/filebeans/deploy/folders-8dd77d7`. The broker restart locked the
  existing protected folder; its existing key was not exported or replaced.
  Quantum and normalizer images were preserved by this deployment.
- Focused broker recovery tests, local/Samba FUSE lifecycle tests, source-scoped
  Trash API tests, and the earlier companion race/vet checks passed. Live disposable
  VPS/Samba file and folder probes verified delete, retained payload bytes, flat
  Trash listing, restore, repeated delete and permanent deletion. All probes
  were cleaned. Source Trash requests returned in approximately 15 milliseconds.
- App changes include visible batch actions, protected-root actions, full-row
  Trash navigation, source-aware Trash loading, formatting-tolerant checksummed
  recovery imports and Universal Clipboard support. No key format or deployment
  target change is required.

### Global protection and location privacy deployment (2026-11-18)

The VPS runs broker/companion/Quantum source
`ba75bb70867468a9e4a5900d88d2167ebe9c0383`. Prepared artifacts were built on
VM-laptop, checksum-verified and loaded on the VPS without building there.

- Companion: `filebeans-companion:ba75bb7-protection`, manifest
  `sha256:23948fcd825fa8e13aeee56a6a42e928ee06fd67d3dfdabcd06735fb30dcd93d`;
  archive SHA-256
  `3f93b96b939c8874905b6e1859e2c705fcce48eb771a0031fd7af978704dba5c`.
- Quantum: `filebeans-quantum:ba75bb7-protection`, manifest
  `sha256:ad86639c4985436fb8c2de39ec4ccfcec7b2631376319de9e4a2099b7c67244e`;
  archive SHA-256
  `885c0b93a00215ca895fe1ae935991b38d6fbe24a902782fa781085afee1341a`.
- Running broker executable SHA-256:
  `009a52f3bfd7f65d49217d23479e7a7c197a5bbd846e2110c94f8454bab8a409`.
- Matching consumers were restarted through the existing protected-stack unit.
  The normalizer retains `filebeans-normalizer:fed272d-parallel` and its resource
  configuration. All three containers were running with zero restart loops.
- Live authenticated HTTPS checks verified aggregate locked state, redacted
  locked-folder descriptors, global Lock and creation capability. The Quantum
  frontend returned HTTP 200. Disposable ordinary VPS/Samba file and folder checks
  verified retained Trash payloads, listing, restoration and permanent deletion;
  all probes were cleaned up. Real-key unlock remains a user-device action.

Deployment artifacts, prior broker/configuration and both prior Compose files
are retained in root-only `/opt/services/filebeans/deploy/protection-ba75bb7`.
No new plaintext registry backup was made. The existing root registry snapshot
was moved to `/var/lib/filebeans-vaults/location-backups/folders-8dd77d7.registry.json`;
its old deployment path is a symlink to that same file. This root-owned location
is configured in `locationBackups`. The first authenticated unlock migrates the
live version-1 registry and this snapshot to encrypted location metadata using
the existing key. Before that unlock, legacy location metadata remains plaintext.
After migration, do not roll back to a version-1-only broker.

Apple validation [44391292854](https://github.com/agawlbb/fileBeans/actions/runs/44391292854)
passed all **103 simulator tests**, with zero failures, and the unsigned Release
archive for iPhone/iPad. Logs verify Xcode **27.2**, device SDK **27.2** and
simulator SDK **27.2**. The source is pinned by tag `testflight/0.1.0-22`.

Signed upload [44394355285](https://github.com/agawlbb/fileBeans/actions/runs/44394355285)
completed successfully for **0.1.0 (22)** from the same tag/source. The log reports
`ARCHIVE SUCCEEDED`, `EXPORT SUCCEEDED` and `UPLOAD SUCCEEDED with no errors`,
and verifies both SDKs at **27.2**. This release includes global protection
controls, imported-key unlock, iPad navigation and encrypted location metadata.
Physical-device biometric interaction and the user's real-key migration remain
user-device acceptance checks.

Apple build `f4a55d28-c3b4-4132-9cd5-e3d4e6ee7022` is **VALID** and
**IN_BETA_TESTING**, with existing automatic internal tester notification enabled.
External state is `READY_FOR_BETA_SUBMISSION`; no tester groups or external review
settings were changed. Deployment and the authorized TestFlight release are
finished and verified.

### Standalone filesystem API trial and TestFlight 23 (2026-11-18)

This is an owner-authorized trial on `feature/filesystem-api`; main was not
merged or changed. Runtime and app source are
`8af23930b55bad98ddb2d6363bda931f0e8cca20`, pinned by `testflight/0.1.0-23`.

- [Apple validation 44407837321](https://github.com/agawlbb/fileBeans/actions/runs/44407837321)
  passed all **104 simulator tests**, with zero failures, and the unsigned device
  Release archive. Xcode/device/simulator SDK versions are **27.2**.
- [Signed upload 44410395658](https://github.com/agawlbb/fileBeans/actions/runs/44410395658)
  reports archive/export success and `UPLOAD SUCCEEDED with no errors` for
  **0.1.0 (23)**. Apple build `4cf1e3a5-5f67-40fc-8e0a-15922baf2ba6` is
  **VALID / IN_BETA_TESTING**. Existing automatic internal notification remains
  enabled; external state is `READY_FOR_BETA_SUBMISSION`. No tester groups,
  branch protections or external review settings were changed.
- The VPS runs `filebeans-api:8af2393-trial`, built on VM-laptop with independent
  Debian/FFmpeg/Poppler/rclone runtime assets. Manifest:
  `sha256:21fda110568b09ea9e8d42aced43840c13c9f507a1a820b909929f6c43ec6052`.
  Image archive SHA-256:
  `8dae096bcfe1e8b97919c227bd5d046bad1b8e7cd5143d02f07b3535f5b72d8e`.
  The transferred archive and loaded image identity were verified. The matching
  broker binary was checked against the VM artifact manifest.
- Quantum is stopped, with its image, configuration and private database retained
  for rollback. The broker's `filesystemConsumer` mode uses the API's control UID
  for readiness. Protected folders are locked following broker restart; real-key
  unlock and physical-device acceptance remain user-device checks.
- A dedicated `filesystem-trial.json` and companion-only Compose overlay leave
  the normalizer's configuration untouched. The temporary cache override for
  source `VPS`, path `/pics`, stores ordinary thumbnails/scratch in private API
  state outside the scan. Protected thumbnails still stay inside encryption.
  Remove this override after the old worker is retired; no originals need moving.
- A trial systemd drop-in excludes the normalizer from stack stop/start commands.
  The worker remains on `filebeans-normalizer:fed272d-parallel`. Container
  `a1e4d4109975`, start time, restart count and PID **2167339** stayed unchanged;
  the same FFmpeg process **2188617** survived. All **743** normalization job IDs
  and **454** previously completed records were retained. Live progress advanced
  during and after cutover; no running conversion was restarted by deployment.
- Go race/vet checks, isolated local/CIFS encrypted-folder tests and the built
  runtime's synthetic API/renderer tests passed. Live authenticated HTTPS checks
  covered source discovery, permissions, ranges, conflicts, thumbnails and
  synthetic VPS/Samba create/upload/edit/rename/Trash/restore/purge. Disposable
  probes were removed. `/pics` thumbnails were confirmed outside the watched
  tree. API restart count and OOM count were zero during verification.

VM artifacts remain in this feature worktree's
`.build/filesystem-runtime/8af23930b55bad98ddb2d6363bda931f0e8cca20/` and
`.build/filesystem-images/8af2393/`. Root-only VPS rollback files, original
configuration/broker, images, worker snapshots and deployment scripts are retained
under `/opt/services/filebeans/deploy/filesystem-8af2393/`.

For a requested rollback during the trial, the retained
`bundle/deploy.py rollback` restores the prior broker and companion/Quantum
services without issuing normalizer stop/start commands. It preserves the worker
present at rollback time. Reinstall the previous native build separately when
requested. No SQL migration or ciphertext conversion was performed.

Trial deployment and the authorized TestFlight release are finished and verified;
physical-device acceptance and the owner's permanent-merge decision remain open.


### Copy correction and temporary Samba thumbnail suspension (2026-11-18)

The owner stopped the API during diagnosis, then requested the copy fix, temporary
Samba thumbnail suspension, deployment and restart. Source
`eda5913c22be1908bee4ef924d038e6205a99c29` remains isolated on the filesystem API
feature branch. Main was not changed.

- Single-file copies no longer receive directory filter flags, which rclone
  rejects. Folder copies still exclude root/nested thumbnail caches and preserve
  empty user folders. Critical-level errors are now surfaced through redaction.
- `disabledThumbnailSources: ["Samba"]` is set in the private API configuration.
  Metadata advertises no previews for that source; thumbnail requests return
  before mount/cache/original access. Existing cache files are retained. The
  ordinary `/pics` cache override remains unchanged.
- VM Go race tests and vet passed. The exact pinned rclone exercised single-file
  copies, nested folder copies, empty folders, cache exclusions and real critical
  error output without requiring SQL. The built image passed those tests and
  ordinary API/renderer/thumbnail-policy regressions in an isolated VM container.
- Deployed `filebeans-api:eda5913-copyfix`, VM-built and checksum-verified.
  Manifest: `sha256:2afee6084e8545b7fd80201aa81d7cb6754357098268ac983acc855c5b5f1266`.
  Archive SHA-256:
  `8ffd3fb0bcca690ef2a389f59cf6fa131694c7bbee4dd8575c82598baea0df9d`.
  Only the companion/API container was recreated with `--no-build --no-deps`;
  the normalizer container identity, start time, PID, image and restart count,
  and the broker PID stayed unchanged.
- Live authenticated HTTPS and VPS-only single-file/folder copy jobs passed.
  The reported failed copy was retried only after checking its original request
  and absent destination. It completed; source metadata remained unchanged and
  source/destination content hashes matched. Synthetic probes were removed.
  No Samba files were created or modified by verification.
- Existing TestFlight **0.1.0 (23)** is compatible. No Apple build or upload was
  needed for this server-only correction.

Prepared archives, previous API configuration/overlay and worker snapshots are
retained under root-only `/opt/services/filebeans/deploy/copyfix-eda5913/`.
`verification.json` records live checks. The API is running; the worker and broker
were not restarted. This requested deployment is finished and verified.

### VPS video/PDF renderer launch correction (2026-11-18)

Source `e58aa165f3d39448e1872d50425e05e3db2c1447` corrects the subprocess
working directory without weakening the API's `PR_SET_DUMPABLE=0` policy. The
renderer now resolves its own inherited directory descriptor before Go remaps
ExtraFiles, rather than trying to access the protected parent's `/proc` entries.
Input and scratch/cache directory descriptors, broker leases, encrypted storage
and bounded renderer concurrency remain in place.

- A helper-process regression reproduced the old launch failure with process
  privacy enabled, then passed for real video and PDF previews with the fix.
  It also checks that the API/helper privacy setting remains disabled for dumps
  after rendering. Go race/vet checks and isolated local/CIFS encrypted-folder
  tests passed. The exact unprivileged runtime image passed the same regression
  and ordinary API/preview-policy checks.
- VM-built image: `filebeans-api:e58aa16-previews`. Manifest:
  `sha256:0e0426119dd5520a525d0cd133882d55aa663cd4491a9108c93de4da58c28eb9`.
  Archive SHA-256:
  `db17b3ba1c9e0f269f80fa5b15004cfe9240810aa1a875a2ea179d5908a61cd6`.
  Transfer checksum and loaded image identity were verified before cutover.
- Only the API container was recreated. Its private configuration was preserved
  byte-for-byte: Samba thumbnail generation remains disabled and ordinary `/pics`
  thumbnails/scratch still use private state outside the normalization scan.
  Worker container ID, PID, start time, image/restart count and broker PID were
  checked unchanged.
- Live checks generated JPEG previews for three existing VPS videos in distinct
  folders, verified cache files in private state, verified matching cached
  responses and confirmed original inode/size/modification time remained
  unchanged. No Samba originals were accessed by these checks. Authenticated
  HTTPS source discovery and Samba preview rejection passed.

Rollback overlay/config snapshots, the prepared image/metadata, worker snapshots
and `verification.json` are retained under root-only
`/opt/services/filebeans/deploy/previews-e58aa16/`. Source stays on the feature
branch; main was not changed. No app build or release was needed; TestFlight 23
is compatible. Reopen a video folder to start new preview requests after an
already-visible thumbnail task failed before the update.

Requested correction and API deployment are finished and verified. Remaining
VPS thumbnails are generated on demand as the owner browses; no bulk archive scan
or background thumbnail worker was started.

## Adjacent VPS and Samba thumbnail caches — November 19, 2026 UTC

At the owner's request, deployed feature source
`7d68475a5478004d24a3c02e7fbb6135b09e3809` as
`filebeans-api:7d68475-thumbnails`. Removed the obsolete
`thumbnailCacheOverrides` and `disabledThumbnailSources` keys from the private
API configuration. All folders now use adjacent `.filebeans-thumbnails` caches;
Samba previews are enabled. No app build or release was needed.

- Prepared and built the API image on VM-laptop. Manifest:
  `sha256:ba30d5a04540b779d1ae81d2e22861888fc89ce0f14947a02a28a7283f7e07af`.
  Archive SHA-256:
  `539d29515c71e6a664469ca19fc57fc06ffd53b498c0a9c042c4435640fc0ebd`.
  Verified the transferred archive and loaded image identity before cutover.
- Recreated only `companion` with `--no-build --no-deps`. The initial health
  check incorrectly used host loopback, causing automatic API rollback; the
  corrected Docker-network check passed on retry.
- Live VPS and Samba video previews produced JPEGs in adjacent hidden caches.
  Repeated responses matched cached bytes; original inode, size and modification
  time were unchanged. API listings hid cache folders. Both video sources passed
  HTTP range requests, and authenticated public HTTPS source discovery passed.
- The separately deployed `filebeans-normalizer:3644c21-recovery` container ID,
  PID, start time, image, command and restart count were unchanged across API
  deployment and verification. The broker process was also preserved. This
  deployment made no encoder or main-branch changes.

Rollback image/configuration snapshots and verification reports are retained in
root-only `/opt/services/filebeans/deploy/thumbnails-7d68475/` and
`/opt/services/filebeans/deploy/thumbnails-7d68475-retry1/`. Restore the saved API
configuration/overlay and recreate only `companion` to roll back; preserve the
normalizer and media/cache files. Thumbnails continue to generate on demand as
folders are browsed; no bulk media scan was started.


## Permanent API acceptance and Quantum retirement — November 19, 2026 UTC

The owner accepted the filesystem API after successful copies, deletes,
thumbnail generation and streaming, and explicitly requested its merge into
main, removal of the feature branch/worktree, and Quantum removal from the VPS.
Merged `feature/filesystem-api` through `bab6a65`, preserving main's encoder
recovery, picture-priority scheduling, and standalone cache exclusions. The
single scan conflict was resolved in favor of main's `walkNormalizationOriginals`
implementation. The merged backend passed Go tests with race detection and vet;
Swift/Python/plist/assets/project structure and isolation validation passed.
No app build or API/encoder image deployment was required for this acceptance.
The observed stale locked-folder listing in Apple Files is deferred at the
owner's request.

On the VPS, removed the already stopped `filebrowser-quantum` container and all
five installed Quantum image tags (`filebeans-quantum` versions and the upstream
stable image). Retired `/opt/services/filebrowser/compose.yaml` into root-only
`/opt/services/filebeans/deploy/quantum-retired-20261119/`. Preserved all media,
encrypted storage, Quantum database/configuration files and historical recovery
archives. The former Quantum proxy fallback now returns HTTP 404; the native
`/companion` proxy route is unchanged. Caddy configuration validation/reload and
authenticated HTTPS source discovery passed. API and normalizer container IDs,
images, PIDs, start times and restart counts remained unchanged. The retirement
directory retains previous proxy/Compose definitions, container metadata, and
`verification.json`.

The feature worktree's ignored `.build` artifacts are retained under the main
checkout's `.build/accepted-filesystem-worktree-artifacts/` before removal of the
worktree and merged local/remote feature branch. Historical source commits and
release records remain reachable from main.

## Settings and Files privacy corrections — TestFlight 24 (2026-11-19)

Released **0.1.0 (24)** from main source
`8f346f9464cbb8ca1ef62fb7d2c3f0badbde0ee6`, pinned by
`testflight/0.1.0-24`.

- [Validation 44441308287](https://github.com/agawlbb/fileBeans/actions/runs/44441308287)
  passed all **107 simulator tests**, with zero failures, and the unsigned
  iPhone/iPad Release archive using Xcode 27.2.
- [Signed publication 44443690658](https://github.com/agawlbb/fileBeans/actions/runs/44443690658)
  passed signing preflight, archive/export, TestFlight upload, and temporary
  credential cleanup. The upload log explicitly reports success with no errors.
- Apple build `65f242fb-d196-44c4-a6bc-e5826b3051c7` is **VALID /
  IN_BETA_TESTING**. Automatic internal notification remains enabled. The
  existing internal group retains access to all builds; no audience settings
  or signing credentials were changed. External state is
  `READY_FOR_BETA_SUBMISSION`; no external review was submitted.
- Protected folders and descendants are excluded from Apple Files even when
  unlocked, including cached identifier operations and working-set changes.
  iPad sidebar taps replace the active detail stack; photo/video upload uses
  a picker attached to the browser. Key and Drive connection tools are in
  Settings, with Lock/Unlock first. Transfers remains in folder top bars and
  moves to the Sources top bar. Protected-folder creation is hidden while locked.

The requested TestFlight release is finished and verified. No VPS deployment
was needed. Physical-device interaction checks remain in `docs/TESTING.md`.
