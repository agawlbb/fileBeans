# Signing and TestFlight

The public app identity is recorded in `Config/Release.json` and `Config/Project.xcconfig`:

| Setting | Value |
| --- | --- |
| App | myFiles |
| Main bundle ID | `com.agawlbb.myfiles` |
| Files extension | `com.agawlbb.myfiles.FileProvider` |
| Shared App Group | `group.com.agawlbb.myfiles` |
| Shared Keychain group | `FKR675M7PM.com.agawlbb.myfiles` |
| App Store SKU | `myfiles-ios` |
| Primary locale | English (U.S.) |

## Current setup

Both bundle IDs are registered with App Groups enabled. The GitHub `testflight` environment is restricted to the `main` branch. Its Apple team/bundle variables and five API/P12 credential secrets are configured. The reusable distribution identity expires November 15, 2027; its public certificate ID is recorded in release configuration.

The encrypted private key, P12, password and reuse helper are permanently retained in the VM's private Apple store, accessible read-only inside development. The installed `apple-signing inspect` command verifies them. **Do not delete the retained files after configuring GitHub or create a certificate for each project.** Future projects should use the shared workspace signing-reuse instructions. This repository contains no private signing material.

Two website registrations remain before provisioning profiles can be finalized:

1. In [Apple Developer identifiers](https://developer.apple.com/account/resources/identifiers/list), register `group.com.agawlbb.myfiles` as an App Group and assign it to **both** bundle IDs above.
2. In [App Store Connect Apps](https://appstoreconnect.apple.com/apps), create the iOS app record with the name, main bundle ID, SKU and primary locale above. Apple's public API cannot create this initial app record.

Once those registrations are complete, run the setup helper from the VM:

```bash
dev node /home/dev/projects/myFiles/scripts/apple-setup.mjs inspect
dev node /home/dev/projects/myFiles/scripts/apple-setup.mjs profiles
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

When refreshing credentials for this project, the shared VM helper installs the retained identity directly into the verified main-only environment. There is no workflow-based extraction from other repositories and no credential artifact transfer. Temporary copies on ephemeral GitHub runners are removed after use; the VM originals remain intact.

## Manual workflow

Build verification on November 15, 2026: source `e335203` passed all 19 tests and produced an unsigned device Release archive for version **0.1.0 (1)**. The [successful run](https://github.com/agawlbb/myFiles/actions/runs/43734190564) includes the archive artifact. Its downloaded artifact digest matched GitHub, and both bundle identifiers, the embedded Files extension, iOS 18 minimum, and iPhone/iPad support were verified. The archive SHA-256 is `b2fd58c41e00a24c64a61c4358b58b91b1c8853814f9735c497748d2fcfd5876`. It has no distribution signature and has not been uploaded to TestFlight.

The TestFlight workflow has three explicit operations:

- **preflight** (default): Ubuntu-only credential, profile, App Group, Keychain, app-record and certificate checks. No build or upload.
- **build**: after preflight passes, create a signed archive/IPA on GitHub macOS and retain the IPA for seven days. No upload.
- **upload**: create the signed package and upload it to TestFlight. Requires release authorization; existing beta-group settings may distribute the uploaded build.

A custom positive build number is optional; otherwise the workflow run number is used. An already-uploaded number is rejected. Run Validate iOS and the [device acceptance checks](TESTING.md) before an authorized release. The workflow does not configure testers, submit an App Store release, or change tester groups.

The workflows use Xcode 26.3 on `macos-15`, meeting Apple's [Xcode 26 / iOS 26 SDK upload requirement](https://developer.apple.com/news/upcoming-requirements/?id=04282026a), while targeting iOS 18+. Signed-device checks and TestFlight upload remain pending. Verify privacy answers, the privacy manifest and encryption declaration against the final shipped behavior before release.
