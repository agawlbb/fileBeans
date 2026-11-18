#!/usr/bin/env python3
"""Install the pinned static rclone dependency; run inside development."""
import argparse
import hashlib
import shutil
from pathlib import Path
import urllib.request
import zipfile

VERSION = '1.76.0'
ARCHIVE = f'rclone-v{VERSION}-linux-amd64.zip'
SHA256 = 'b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('output', type=Path, help='Directory receiving rclone and rclone-README.txt')
    parser.add_argument('--archive', type=Path, help='Reuse a local archive; the pinned checksum is still required')
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    archive = args.output / ARCHIVE
    if args.archive is not None:
        if hashlib.sha256(args.archive.read_bytes()).hexdigest() != SHA256:
            raise SystemExit('rclone archive checksum mismatch; refusing to install')
        if args.archive.resolve() != archive.resolve():
            shutil.copyfile(args.archive, archive)
    if not archive.exists():
        with urllib.request.urlopen(f'https://downloads.rclone.org/v{VERSION}/{ARCHIVE}', timeout=120) as response:
            archive.write_bytes(response.read())
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SHA256:
        raise SystemExit('rclone archive checksum mismatch; refusing to install')
    with zipfile.ZipFile(archive) as package:
        prefix = f'rclone-v{VERSION}-linux-amd64/'
        (args.output / 'rclone').write_bytes(package.read(prefix + 'rclone'))
        (args.output / 'rclone-README.txt').write_bytes(package.read(prefix + 'README.txt'))
    (args.output / 'rclone').chmod(0o755)
    print(f'Installed static rclone {VERSION}; verified SHA-256 {SHA256}')


if __name__ == '__main__':
    main()
