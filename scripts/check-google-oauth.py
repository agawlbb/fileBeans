#!/usr/bin/env python3
"""Validate public native Google registration before a signed app build."""
import argparse
import os
from pathlib import Path
import re


def registration(client_id):
    if not re.fullmatch(r"[A-Za-z0-9-]+\.apps\.googleusercontent\.com", client_id):
        raise ValueError("Set GOOGLE_IOS_CLIENT_ID to the public Google iOS OAuth client ID registered for com.agawlbb.myfiles")
    return client_id, ".".join(reversed(client_id.split(".")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--export-env", action="store_true")
    args = parser.parse_args()
    value = os.environ.get("GOOGLE_IOS_CLIENT_ID", "")
    if not value:
        config = Path(__file__).resolve().parents[1] / "Config/Project.xcconfig"
        match = re.search(r"^GOOGLE_IOS_CLIENT_ID[ \t]*=[ \t]*(\S*)[ \t]*$", config.read_text(), re.MULTILINE)
        value = match.group(1) if match else ""
    try:
        client, scheme = registration(value)
    except ValueError as error:
        parser.exit(1, f"Native Google sign-in configuration missing: {error}\n")
    if args.export_env:
        path = os.environ.get("GITHUB_ENV")
        if not path:
            parser.exit(1, "GITHUB_ENV is required for --export-env\n")
        with Path(path).open("a") as stream:
            stream.write(f"GOOGLE_IOS_CLIENT_ID={client}\nGOOGLE_IOS_REVERSED_CLIENT_ID={scheme}\n")
    print("Public native Google OAuth client registration validated")


if __name__ == "__main__":
    main()
