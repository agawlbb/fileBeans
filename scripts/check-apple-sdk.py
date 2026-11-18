#!/usr/bin/env python3
"""Fail a build before compilation when its selected Apple SDK is too old."""
import re
import subprocess

MINIMUM_SDK = 27

def require_current_sdk(version, platform):
    if not re.fullmatch(r"\d+(?:\.\d+){0,2}", version):
        raise ValueError(f"Unrecognized {platform} SDK version: {version}")
    if int(version.split(".")[0]) < MINIMUM_SDK:
        raise ValueError(f"{platform} SDK {MINIMUM_SDK} or later required; selected {version}")
    return version

def main():
    subprocess.run(["xcodebuild", "-version"], check=True)
    for platform in ("iphoneos", "iphonesimulator"):
        version = subprocess.check_output(
            ["xcrun", "--sdk", platform, "--show-sdk-version"], text=True).strip()
        require_current_sdk(version, platform)
        print(f"Verified {platform} SDK {version}")

if __name__ == "__main__":
    main()
