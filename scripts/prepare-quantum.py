#!/usr/bin/env python3
"""Prepare the pinned Quantum dependency and local vault adapter; no image build."""
import argparse
import hashlib
import pathlib
import shutil
import subprocess
import tarfile
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]
REVISION = "2c071d1e6eb48d4b88da2657885d345663a0a88b"
SHA256 = "f9f783bda505d47ff86b290519338c8b4f28069bb2a29f8e35ad56bc66de7a1e"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, default=ROOT / ".build/quantum/source")
    args = parser.parse_args()
    target = args.output.resolve()
    if not target.is_relative_to(ROOT / ".build"):
        parser.error("dependency preparation must stay inside .build")
    archive = ROOT / ".build/quantum/upstream.tar.gz"
    archive.parent.mkdir(parents=True, exist_ok=True)
    if not archive.exists():
        urllib.request.urlretrieve(f"https://codeload.github.com/gtsteffaniak/filebrowser/tar.gz/{REVISION}", archive)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SHA256:
        raise SystemExit("Quantum archive checksum mismatch")
    if target.exists():
        if not (target / ".filebeans-prepared").exists():
            raise SystemExit("Refusing to replace an unowned dependency directory")
        shutil.rmtree(target)
    target.mkdir(parents=True)
    (target / ".filebeans-prepared").write_text(REVISION + "\n")
    with tarfile.open(archive) as contents:
        for member in contents.getmembers():
            prefix, _, relative = member.name.partition("/")
            if prefix != "filebrowser-" + REVISION:
                raise SystemExit("Unexpected archive root")
            if not relative:
                continue
            member.name = relative
            contents.extract(member, target, filter="data")
    subprocess.run(["python3", str(ROOT / "quantum/patch.py"), str(target)], check=True)
    shutil.copytree(ROOT / "quantum/adapter/vaultbridge", target / "backend/pkg/vaultbridge")
    shutil.copyfile(ROOT / "quantum/adapter/vault_preview.go", target / "backend/internal/preview/vault_preview.go")
    shutil.copyfile(ROOT / "quantum/adapter/vault_integration_test.go", target / "backend/internal/web/vault_integration_test.go")
    module = target / "backend/go.mod"
    module.write_text(module.read_text() + f"\nrequire filebeans/companion v0.0.0\nreplace filebeans/companion => {ROOT / 'companion'}\n")
    print(f"Prepared Quantum {REVISION}: {target}")


if __name__ == "__main__":
    main()
