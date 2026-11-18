#!/bin/sh
# VM-local runtime control only. Images were built by vm-image.py, never here.
set -eu
if [ "$#" -ne 3 ]; then
    echo 'Usage: sh scripts/test-vault-images.sh QUANTUM.tar COMPANION.tar NORMALIZER.tar' >&2
    exit 2
fi
project=/home/dev/projects/fileBeans
# Verify archives against the VM builder's metadata before loading them. This
# emits only image tags, never configuration, credentials or container logs.
tags=$(python3 - "$@" <<'PY'
import hashlib,json,pathlib,sys
for argument in sys.argv[1:]:
    archive=pathlib.Path(argument).resolve()
    if not archive.is_relative_to(pathlib.Path('/home/dev/projects/fileBeans/.build/images')):
        raise SystemExit('Only prepared project image archives are accepted')
    metadata=json.loads(archive.with_suffix(archive.suffix+'.json').read_text())
    with archive.open('rb') as data:
        if hashlib.file_digest(data,'sha256').hexdigest()!=metadata['sha256']:
            raise SystemExit('Image archive checksum mismatch')
    if len(metadata['tags'])!=1 or metadata['platform']!='linux/amd64':
        raise SystemExit('Unexpected image metadata')
    tag=metadata['tags'][0]
    if any(c.isspace() for c in tag):
        raise SystemExit('Invalid image tag')
    print(tag)
PY
)
for archive in "$@"; do sudo docker load --input "$archive"; done
FILEBEANS_QUANTUM_IMAGE=$(printf '%s\n' "$tags" | sed -n '1p')
FILEBEANS_COMPANION_IMAGE=$(printf '%s\n' "$tags" | sed -n '2p')
FILEBEANS_NORMALIZER_IMAGE=$(printf '%s\n' "$tags" | sed -n '3p')
export FILEBEANS_QUANTUM_IMAGE FILEBEANS_COMPANION_IMAGE FILEBEANS_NORMALIZER_IMAGE
for service in quantum companion normalizer; do
    sudo --preserve-env=FILEBEANS_QUANTUM_IMAGE,FILEBEANS_COMPANION_IMAGE,FILEBEANS_NORMALIZER_IMAGE \
        docker compose -p filebeans-vault-image-tests \
        -f "$project/companion/deploy/compose.vault-image-tests.yaml" \
        run --rm --no-deps --pull never "$service"
done
