# GitHub macOS and TestFlight

Create an App Store Connect app record for the chosen main bundle ID. Register the extension ID (`MAIN_ID.FileProvider`) and App Group (`group.MAIN_ID`). Both targets must share Keychain access (`TEAM_ID.MAIN_ID`) and the App Group. Produce App Store distribution provisioning profiles for both targets using the same distribution certificate. Existing Apple access does not automatically create these app-specific records or profiles.

Configure the GitHub environment `testflight` with variables:

| Variable | Value |
| --- | --- |
| `APPLE_TEAM_ID` | Apple Developer team identifier |
| `BUNDLE_ID_PREFIX` | Registered main bundle identifier |

Configure environment secrets (base64 values must be single-line):

| Secret | Value |
| --- | --- |
| `DISTRIBUTION_P12_BASE64` | Distribution certificate and private key exported as a P12 |
| `DISTRIBUTION_P12_PASSWORD` | Its export password |
| `APP_PROFILE_BASE64` | App Store profile for the main app |
| `PROVIDER_PROFILE_BASE64` | App Store profile for the Files extension |
| `ASC_PRIVATE_KEY_BASE64` | App Store Connect API private key |
| `ASC_KEY_ID` | API key identifier |
| `ASC_ISSUER_ID` | API issuer identifier |

Use an App Store Connect API key with permission to upload this app. Configure environment protection if release approval is desired. Secrets are never needed for pull-request simulator checks. The release workflow uses an ephemeral runner Keychain and removes signing files even after failure. It neither invites testers nor changes tester groups.

When a build/release is authorized, run **Validate iOS** first. After it passes and device checks are complete, dispatch **TestFlight** for the approved commit. The workflow archives both targets, exports an IPA, and uploads it. The GitHub run number becomes the build number; it must exceed any existing build number in App Store Connect. Processing and any required beta review happen in App Store Connect. Do not dispatch merely to test whether credentials exist.

The workflows select Xcode 26.3 on `macos-15`, meeting Apple's [Xcode 26 / iOS 26 SDK upload requirement](https://developer.apple.com/news/upcoming-requirements/?id=04282026a). The deployment target remains iOS 18. If that image retires the toolchain, update both workflows together after checking SDK availability. XcodeGen generates the shared scheme and both targets from `project.yml`.

App Store privacy answers should reflect direct connections to user-selected servers and local caching. Verify `ITSAppUsesNonExemptEncryption` and the privacy manifest against the final shipped behavior before release. The app currently uses standard HTTPS provided by Apple's frameworks, without custom cryptography.
