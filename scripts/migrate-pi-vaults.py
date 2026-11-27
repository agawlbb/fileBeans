#!/usr/bin/env python3
"""Offline, metadata-only handoff. Never mounts vaults or accepts recovery keys."""
import argparse
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import tempfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_regular(path, limit=4 << 20):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise ValueError("Expected a regular metadata file")
        data = stream.read(limit + 1)
    if len(data) > limit:
        raise ValueError("Metadata exceeds limit")
    return data


def atomic(path, data, replace=False):
    path = Path(path)
    if path.exists() and not replace:
        raise ValueError("Output already exists")
    fd, temporary = tempfile.mkstemp(prefix=".handoff-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if replace:
            os.replace(temporary, path)
        else:
            os.link(temporary, path)
        parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def encoded(value):
    return (json.dumps(value, separators=(",", ":"), sort_keys=True) + "\n").encode()


def root_for(config, source):
    matches = [Path(r["path"]) for r in config["roots"] if r["name"] == source]
    if len(matches) != 1 or matches[0].resolve() != matches[0]:
        raise ValueError("Expected one physical source root without symlinks")
    return matches[0]


def validate_entries(entries):
    seen = set()
    if not entries:
        raise ValueError("No vaults selected")
    for entry in entries:
        identity = entry["id"]
        if not re.fullmatch(r"[0-9a-f]{32}", identity) or identity in seen:
            raise ValueError("Invalid or duplicate vault identity")
        seen.add(identity)
        if not entry.get("parked") or entry.get("parking") or entry.get("purging"):
            raise ValueError("Vaults must be locked and fully parked before handoff")
        if not entry.get("location") or any(entry.get(k) for k in ("path", "originalPath", "previousPaths", "nextPath")):
            raise ValueError("Expected sealed location metadata")


def storage_identity(root, identity):
    store = root / ".filebeans-vaults"
    marker = store / (identity + ".public")
    cipher = store / identity
    for path in (store, marker, cipher):
        if path.resolve() != path or not stat.S_ISDIR(path.lstat().st_mode):
            raise ValueError("Invalid vault storage or marker")
    info = marker.stat()
    if info.st_dev != root.stat().st_dev or os.path.ismount(marker) or list(marker.iterdir()):
        raise ValueError("Marker must be empty, unmounted and on the source filesystem")
    if os.path.ismount(cipher):
        raise ValueError("Ciphertext must not be a mounted plaintext directory")
    return info, digest(read_regular(cipher / "gocryptfs.conf"))


def run(args):
    config = json.loads(read_regular(Path(args.config)))
    state = Path(config["state"])
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    if state.resolve() != state:
        raise ValueError("Broker state must not use symlinks")
    lock = os.open(state / "broker.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        registry_path = state / "registry.json"
        if args.action == "export":
            raw = read_regular(registry_path)
            registry = json.loads(raw)
            if registry.get("version") != 2:
                raise ValueError("Registry must use sealed locations")
            entries = [e for e in registry["entries"] if e["source"] == args.source]
            validate_entries(entries)
            root = root_for(config, args.source)
            hashes = {e["id"]: storage_identity(root, e["id"])[1] for e in entries}
            remaining = copy.deepcopy(registry)
            remaining["entries"] = [e for e in registry["entries"] if e["source"] != args.source]
            key_id = read_regular(state / "vault-key-id", 64).decode()
            if not re.fullmatch(r"[0-9a-f]{64}", key_id):
                raise ValueError("Invalid key identifier")
            bundle = {"version": 1, "source": args.source, "registryHash": digest(raw),
                      "keyId": key_id, "entries": entries, "cipherConfigs": hashes,
                      "remainingHash": digest(encoded(remaining))}
            atomic(args.output, encoded(bundle))
            print(f"Exported {len(entries)} sealed vault records; no source ownership changed")
            return
        bundle_raw = read_regular(Path(args.bundle))
        bundle = json.loads(bundle_raw)
        if bundle.get("version") != 1:
            raise ValueError("Unsupported handoff bundle")
        validate_entries(bundle["entries"])
        if not re.fullmatch(r"[0-9a-f]{64}", bundle["keyId"]):
            raise ValueError("Invalid handoff key identifier")
        if any(e["source"] != bundle["source"] for e in bundle["entries"]):
            raise ValueError("Bundle mixes sources")
        if args.action == "retire":
            raw = read_regular(registry_path)
            if digest(raw) == bundle["remainingHash"] and args.apply:
                if not Path(args.receipt).exists():
                    atomic(args.receipt, encoded({"bundleHash": digest(bundle_raw), "remainingHash": digest(raw)}))
                print("Source retirement already complete; receipt available")
                return
            if digest(raw) != bundle["registryHash"]:
                raise ValueError("Source registry changed since export")
            registry = json.loads(raw)
            root = root_for(config, bundle["source"])
            for e in bundle["entries"]:
                if storage_identity(root, e["id"])[1] != bundle["cipherConfigs"][e["id"]]:
                    raise ValueError("Cipher configuration changed")
            registry["entries"] = [e for e in registry["entries"] if e["source"] != bundle["source"]]
            remaining = encoded(registry)
            if not args.apply:
                print(f"Would retire {len(bundle['entries'])} source records; no changes")
                return
            if Path(args.receipt).exists():
                raise ValueError("Receipt already exists")
            atomic(registry_path, remaining, replace=True)
            atomic(args.receipt, encoded({"bundleHash": digest(bundle_raw), "remainingHash": digest(remaining)}))
            print("Source records retired; source must stay stopped until its root configuration is updated")
            return
        receipt = json.loads(read_regular(Path(args.receipt)))
        if receipt.get("bundleHash") != digest(bundle_raw):
            raise ValueError("Retirement receipt does not match bundle")
        if registry_path.exists():
            raise ValueError("Destination broker state must be new")
        key_path = state / "vault-key-id"
        if key_path.exists() and read_regular(key_path, 64).decode() != bundle["keyId"]:
            raise ValueError("Destination key identifier differs")
        root = root_for(config, bundle["source"])
        entries = copy.deepcopy(bundle["entries"])
        for entry in entries:
            info, config_hash = storage_identity(root, entry["id"])
            if config_hash != bundle["cipherConfigs"][entry["id"]]:
                raise ValueError("Local ciphertext does not match exported identity")
            entry["device"], entry["inode"] = info.st_dev, info.st_ino
        if not args.apply:
            print(f"Would import {len(entries)} vaults with local filesystem identities; no changes")
            return
        if not key_path.exists():
            atomic(key_path, bundle["keyId"].encode())
        atomic(registry_path, encoded({"version": 2, "entries": entries}))
        print(f"Imported {len(entries)} sealed records; vault IDs and locations preserved")
    finally:
        os.close(lock)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    for action in ("export", "retire", "import"):
        command = sub.add_parser(action)
        command.add_argument("--config", required=True)
        if action == "export":
            command.add_argument("--source", required=True)
            command.add_argument("--output", required=True)
        else:
            command.add_argument("--bundle", required=True)
            command.add_argument("--receipt", required=True)
            command.add_argument("--apply", action="store_true")
    try:
        run(parser.parse_args())
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"Handoff refused: {error}\n")


if __name__ == "__main__":
    main()
