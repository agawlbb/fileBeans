"""Prepare bounded, read-only OCI distribution files from the VM-built image."""
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tarfile

source, destination = map(Path, sys.argv[1:])
limit = 96 * 1024 * 1024
written = 0
destination.mkdir(mode=0o700)
try:
    with tarfile.open(source) as archive:
        index = json.load(archive.extractfile('index.json'))
        descriptor = index['manifests'][0]
        digest = descriptor['digest']
        manifest = archive.extractfile('blobs/' + digest.replace(':', '/')).read()
        if 'sha256:' + hashlib.sha256(manifest).hexdigest() != digest:
            raise ValueError('Manifest digest mismatch')
        image = json.loads(manifest)
        for blob in [image['config'], *image['layers']]:
            name = blob['digest']
            if not name.startswith('sha256:') or len(name) != 71:
                raise ValueError('Unsupported digest')
            size = blob['size']
            written += size
            if size < 0 or written > limit:
                raise ValueError('Image exceeds registry preparation limit')
            target = destination / 'blobs' / name
            target.parent.mkdir(exist_ok=True)
            h = hashlib.sha256()
            count = 0
            with archive.extractfile('blobs/' + name.replace(':', '/')) as src, target.open('xb') as out:
                while chunk := src.read(min(1024 * 1024, size - count + 1)):
                    count += len(chunk)
                    if count > size:
                        raise ValueError('Blob exceeds declared size')
                    h.update(chunk)
                    out.write(chunk)
            if count != size or 'sha256:' + h.hexdigest() != name:
                raise ValueError('Blob digest mismatch')
        for tag in ['0.1.0', digest]:
            target = destination / 'manifests' / tag
            target.parent.mkdir(exist_ok=True)
            target.write_bytes(manifest)
    destination.chmod(0o755)
    print(f'Prepared verified image {digest}, {written} blob bytes (96 MiB limit)')
except BaseException:
    shutil.rmtree(destination)
    raise
