#!/bin/sh
# Run on VM-laptop. Only synthetic fixtures are mounted into the disposable
# FUSE/CIFS runtime. No image is built and no production data is accessed.
set -eu
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dev bash -s -- "$project" <<'PREPARE'
set -eu
cd "$1"
go_tool=$(command -v go || true)
if [ -z "$go_tool" ]; then go_tool=/tmp/filebeans-go/go/bin/go; fi
mkdir -p .build/fuse-tests
python3 scripts/prepare-fuse-tools.py
install -m 0444 scripts/smb-fixture.py .build/fuse-tests/smb-fixture.py
install -m 0444 Tests/Fixtures/range-probe.mp4 .build/fuse-tests/range-probe.mp4
# Reuse the canonical checkout's installed test-only Python dependencies.
/home/dev/projects/fileBeans/.venv/bin/python - "$PWD/.build/fuse-tests/python" <<'PY'
import importlib.metadata,pathlib,shutil,site,sys
if importlib.metadata.version('impacket') != '0.13.0':
    raise SystemExit('Install scripts/requirements-smb-tests.txt in the canonical .venv')
for source in site.getsitepackages():
    if pathlib.Path(source).is_dir():
        shutil.copytree(source,sys.argv[1],dirs_exist_ok=True,ignore=shutil.ignore_patterns('__pycache__'))
PY
install -m 0555 /home/dev/.local/bin/gocryptfs .build/fuse-tests/gocryptfs
install -m 0555 /usr/bin/fusermount3 .build/fuse-tests/fusermount3
install -m 0555 /usr/bin/rclone .build/fuse-tests/rclone
cd companion
CGO_ENABLED=0 "$go_tool" test -c -o ../.build/fuse-tests/service.test ./internal/service
chmod 0555 ../.build/fuse-tests/service.test
PREPARE
exec sudo docker compose -p filebeans-filesystem-tests \
 -f "$project/companion/deploy/compose.filesystem-tests.yaml" \
 run --rm --no-deps --pull never filesystem-tests
