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

## Manual workflow

Build verification on November 15, 2026: source `e335203` passed all 19 tests and produced an unsigned device Release archive for version **0.1.0 (1)**. The [successful run](https://github.com/agawlbb/fileBeans/actions/runs/43734190564) includes the archive artifact. Its downloaded artifact digest matched GitHub, and both bundle identifiers, the embedded Files extension, iOS 18 minimum, and iPhone/iPad support were verified. The archive SHA-256 is `b2fd58c41e00a24c64a61c4358b58b91b1c8853814f9735c497748d2fcfd5876`. It has no distribution signature and has not been uploaded to TestFlight.

The TestFlight workflow has three explicit operations:

- **preflight** (default): Ubuntu-only credential, profile, App Group, Keychain, app-record and certificate checks. No build or upload.
- **build**: after preflight passes, create a signed archive/IPA on GitHub macOS and retain the IPA for seven days. No upload.
- **upload**: create the signed package and upload it to TestFlight. Requires release authorization; existing beta-group settings may distribute the uploaded build.

Select the source branch in the workflow’s **Run workflow** menu for any operation; merging into `main` is not required.

A custom positive build number is optional; otherwise the workflow run number is used. An already-uploaded number is rejected. Run Validate iOS and the [device acceptance checks](TESTING.md) before an authorized release. The workflow does not configure testers, submit an App Store release, or change tester groups.

The workflows use Xcode 26.3 on `macos-15`, meeting Apple's [Xcode 26 / iOS 26 SDK upload requirement](https://developer.apple.com/news/upcoming-requirements/?id=04282026a), while targeting iOS 18+. Signed-device checks and TestFlight upload remain pending. Verify privacy answers, the privacy manifest and encryption declaration against the final shipped behavior before release.

Both workflows accept a macOS runner choice. The default remains `macos-15`
(Apple Silicon). If that hosted pool stays queued, `macos-15-intel` uses the same
macOS/Xcode 26.3 toolchain on Intel; simulator tests still precede release.

Validation selects an available iPhone runtime matching the selected Xcode SDK
(or its newest compatible installed runtime). Simulator boot has a separate
ten-minute limit; compilation/tests have twenty minutes. This keeps fresh Intel
runner setup from consuming the entire test execution budget.
