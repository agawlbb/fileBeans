#!/usr/bin/env python3
"""Prepare feature runtime artifacts inside development; never deploy or build images."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.check_output(args, cwd=ROOT, text=True, **kwargs).strip()


def main():
    if run("git", "status", "--porcelain", "--untracked-files=no"):
        raise SystemExit("Commit tracked source changes before preparing versioned runtime artifacts")
    revision = run("git", "rev-parse", "HEAD")
    target = ROOT / ".build" / "vault-runtime" / revision
    if target.exists():
        raise SystemExit("Runtime artifacts already exist for this revision; preserve them")
    go = shutil.which("go") or "/tmp/filebeans-go/go/bin/go"
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
    target.mkdir(parents=True)
    for name, package in [("filebeans", "./cmd/filebeans"), ("filebeans-vault-broker", "./cmd/filebeans-vault-broker")]:
        subprocess.run([go, "build", "-trimpath", "-ldflags=-s -w", "-o", str(target / name), package],
                       cwd=ROOT / "companion", env=env, check=True)
    companion = target / "companion"
    normalizer = target / "normalizer"
    companion.mkdir()
    normalizer.mkdir()
    shutil.copyfile(target / "filebeans", companion / "filebeans")
    shutil.copyfile(target / "filebeans", normalizer / "filebeans")
    shutil.copyfile(ROOT / "companion/deploy/Dockerfile", companion / "Dockerfile")
    shutil.copyfile(ROOT / "companion/deploy/Dockerfile.normalizer", normalizer / "Dockerfile")
    subprocess.run(["python3", str(ROOT / "scripts/prepare-rclone.py"), str(companion)], check=True)
    certs = companion / "etc/ssl/certs"
    certs.mkdir(parents=True)
    shutil.copyfile("/etc/ssl/certs/ca-certificates.crt", certs / "ca-certificates.crt")
    for binary in [companion / "filebeans", normalizer / "filebeans"]:
        binary.chmod(0o555)
    # This is the same standalone tool used by the independent recovery tests.
    gocryptfs = Path.home() / ".local/bin/gocryptfs"
    version = run(str(gocryptfs), "-version")
    if not version.startswith("gocryptfs v2.6.1;"):
        raise SystemExit("Install the tested gocryptfs 2.6.1 before packaging")
    shutil.copyfile(gocryptfs, target / "gocryptfs")
    (target / "gocryptfs").chmod(0o555)
    files = {str(path.relative_to(target)): hashlib.sha256(path.read_bytes()).hexdigest()
             for path in sorted(target.rglob("*")) if path.is_file()}
    (target / "artifacts.json").write_text(json.dumps({"revision": revision, "gocryptfs": version,
                                                     "sha256": files}, indent=2) + "\n")
    print(f"Prepared runtime binaries and image contexts: {target}")


if __name__ == "__main__":
    main()
