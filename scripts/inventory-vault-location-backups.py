#!/usr/bin/env python3
"""Read-only inventory of legacy location snapshots; never prints their contents."""
import argparse
import json
from pathlib import Path

def inventory(roots):
    found = []
    for root in roots:
        root = Path(root).resolve(strict=True)
        for path in root.rglob("*"):
            if path.is_symlink() or not path.is_file():
                continue
            if path.suffix != ".json" and not path.name.startswith(".broker-"):
                continue
            if path.stat().st_size > 32 * 1024 * 1024:
                continue
            try:
                value = json.loads(path.read_bytes())
            except (ValueError, OSError):
                continue
            if not isinstance(value, dict) or value.get("version") != 1:
                continue
            entries = value.get("entries")
            if isinstance(entries, list) and entries and all(
                isinstance(e, dict) and isinstance(e.get("id"), str) and
                isinstance(e.get("source"), str) and isinstance(e.get("path"), str)
                for e in entries
            ):
                found.append(str(path))
    return sorted(set(found))

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("roots", nargs="+", help="Explicit trusted deployment/state directories")
    args = parser.parse_args()
    print(json.dumps(inventory(args.roots), indent=2))

if __name__ == "__main__":
    main()
