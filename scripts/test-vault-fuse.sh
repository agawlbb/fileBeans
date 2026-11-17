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
install -m 0555 /home/dev/.local/bin/gocryptfs .build/fuse-tests/gocryptfs
install -m 0555 /usr/bin/fusermount3 .build/fuse-tests/fusermount3
cd companion
CGO_ENABLED=0 "$go_tool" test -c -o ../.build/fuse-tests/service.test ./internal/service
chmod 0555 ../.build/fuse-tests/service.test
'
exec sudo docker compose -p filebeans-vault-tests \
    -f "$project/companion/deploy/compose.vault-tests.yaml" \
    run --rm --no-deps --pull never vault-tests
