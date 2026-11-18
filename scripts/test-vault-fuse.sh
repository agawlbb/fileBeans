#!/bin/sh
# Run on VM-laptop. Project compilation stays in the normal development container;
# only the fixed synthetic mount/recovery test runs in an isolated FUSE runtime.
set -eu
project=/home/dev/projects/fileBeans
dev bash -lc '
set -eu
cd /home/dev/projects/fileBeans
go_tool=$(command -v go || true)
if [ -z "$go_tool" ]; then go_tool=/tmp/filebeans-go/go/bin/go; fi
test -x "$go_tool"
test -x /home/dev/.local/bin/gocryptfs
test -x /usr/bin/fusermount3
mkdir -p .build/fuse-tests
chmod 0755 .build/fuse-tests
python3 scripts/prepare-fuse-tools.py
install -m 0444 scripts/smb-fixture.py .build/fuse-tests/smb-fixture.py
.venv/bin/python - <<"PY"
import importlib.metadata
import pathlib
import shutil
import site
if importlib.metadata.version("impacket") != "0.13.0":
    raise SystemExit("Install scripts/requirements-smb-tests.txt in .venv")
target = pathlib.Path(".build/fuse-tests/python")
for source in site.getsitepackages():
    if pathlib.Path(source).is_dir():
        shutil.copytree(source, target, dirs_exist_ok=True, ignore=shutil.ignore_patterns("__pycache__"))
PY
install -m 0555 /home/dev/.local/bin/gocryptfs .build/fuse-tests/gocryptfs
install -m 0555 /usr/bin/fusermount3 .build/fuse-tests/fusermount3
cd companion
CGO_ENABLED=0 "$go_tool" test -c -o ../.build/fuse-tests/service.test ./internal/service
chmod 0555 ../.build/fuse-tests/service.test
cd /home/dev/projects/fileBeans
python3 scripts/prepare-quantum.py
cd .build/quantum/source/backend
CGO_ENABLED=0 "$go_tool" test -c -o /home/dev/projects/fileBeans/.build/fuse-tests/quantum.test ./internal/web
chmod 0555 /home/dev/projects/fileBeans/.build/fuse-tests/quantum.test
'
exec sudo docker compose -p filebeans-vault-tests \
    -f "$project/companion/deploy/compose.vault-tests.yaml" \
    run --rm --no-deps --pull never vault-tests
