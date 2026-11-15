#!/usr/bin/env python3
"""Ephemeral macOS runner signing setup. Never emits certificate or API key content."""
import base64
import os
from pathlib import Path
import plistlib
import secrets
import shutil
import subprocess
import sys

root = Path(os.environ["RUNNER_TEMP"]) / "myfiles-signing"
keychain = root / "signing.keychain-db"
profiles = Path.home() / "Library/MobileDevice/Provisioning Profiles"


def run(*args):
    try:
        return subprocess.check_output(args, stderr=subprocess.PIPE)
    except subprocess.CalledProcessError:
        # CalledProcessError's default text contains argv, including signing passwords.
        raise RuntimeError(f"Signing command failed: {args[0]} {args[1]}") from None


def cleanup():
    if keychain.exists():
        subprocess.run(["security", "delete-keychain", str(keychain)], check=False, capture_output=True)
    for path in root.glob("*.installed-profile"):
        destination = Path(path.read_text())
        if destination.parent == profiles:
            destination.unlink(missing_ok=True)
    shutil.rmtree(root, ignore_errors=True)


if "--cleanup" in sys.argv:
    cleanup()
    sys.exit(0)

required = ["APPLE_TEAM_ID", "BUNDLE_ID_PREFIX", "DISTRIBUTION_P12_BASE64", "DISTRIBUTION_P12_PASSWORD",
            "APP_PROFILE_BASE64", "PROVIDER_PROFILE_BASE64", "ASC_PRIVATE_KEY_BASE64", "ASC_KEY_ID"]
for name in required:
    if not os.environ.get(name):
        sys.exit(f"Missing release configuration: {name}")
if os.environ["BUNDLE_ID_PREFIX"].startswith("com.example."):
    sys.exit("Configure a registered bundle identifier before releasing.")
root.mkdir(mode=0o700, parents=True, exist_ok=True)
password = secrets.token_urlsafe(32)
print(f"::add-mask::{password}")
run("security", "create-keychain", "-p", password, str(keychain))
run("security", "set-keychain-settings", "-lut", "21600", str(keychain))
run("security", "unlock-keychain", "-p", password, str(keychain))
certificate = root / "distribution.p12"
certificate.write_bytes(base64.b64decode(os.environ["DISTRIBUTION_P12_BASE64"], validate=True))
certificate.chmod(0o600)
run("security", "import", str(certificate), "-P", os.environ["DISTRIBUTION_P12_PASSWORD"],
    "-A", "-t", "cert", "-f", "pkcs12", "-k", str(keychain))
run("security", "set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-k", password, str(keychain))
run("security", "list-keychains", "-d", "user", "-s", str(keychain), str(Path.home() / "Library/Keychains/login.keychain-db"))
profiles.mkdir(parents=True, exist_ok=True)
mapping = {}
with open(os.environ["GITHUB_ENV"], "a") as env:
    env.write(f"SIGNING_KEYCHAIN={keychain}\n")
    for kind, suffix in [("APP", ""), ("PROVIDER", ".FileProvider")]:
        profile_file = root / f"{kind}.mobileprovision"
        profile_file.write_bytes(base64.b64decode(os.environ[f"{kind}_PROFILE_BASE64"], validate=True))
        profile = plistlib.loads(run("security", "cms", "-D", "-i", str(profile_file)))
        bundle = os.environ["BUNDLE_ID_PREFIX"] + suffix
        expected = os.environ["APPLE_TEAM_ID"] + "." + bundle
        entitlements = profile["Entitlements"]
        if entitlements.get("application-identifier") != expected:
            sys.exit(f"{kind} provisioning profile does not match the configured team and bundle ID.")
        if "group." + os.environ["BUNDLE_ID_PREFIX"] not in entitlements.get("com.apple.security.application-groups", []):
            sys.exit(f"{kind} provisioning profile is missing the shared App Group.")
        if entitlements.get("get-task-allow") or profile.get("ProvisionedDevices"):
            sys.exit(f"{kind} provisioning profile must be for App Store distribution.")
        uuid = profile["UUID"]
        destination = profiles / f"{uuid}.mobileprovision"
        shutil.copyfile(profile_file, destination)
        (root / f"{kind}.installed-profile").write_text(str(destination))
        env.write(f"{kind}_PROFILE_UUID={uuid}\n")
        mapping[bundle] = uuid
    env.write(f"APP_GROUP=group.{os.environ['BUNDLE_ID_PREFIX']}\n")
options = {"method": "app-store-connect", "teamID": os.environ["APPLE_TEAM_ID"], "signingStyle": "manual",
           "provisioningProfiles": mapping, "uploadSymbols": True, "manageAppVersionAndBuildNumber": False}
(root / "ExportOptions.plist").write_bytes(plistlib.dumps(options))
keys = root / "private_keys"
keys.mkdir(mode=0o700, exist_ok=True)
key = keys / f"AuthKey_{os.environ['ASC_KEY_ID']}.p8"
key.write_bytes(base64.b64decode(os.environ["ASC_PRIVATE_KEY_BASE64"], validate=True))
key.chmod(0o600)
