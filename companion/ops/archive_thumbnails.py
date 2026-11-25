#!/usr/bin/env python3
"""Compare archive names and backfill existing thumbnails; never open media data."""
import argparse
import contextlib
import dataclasses
import fcntl
import hashlib
import json
import os
import posixpath
import signal
import stat
import time
import uuid

CACHE = '.filebeans-thumbnails'
INTERNAL = {CACHE, '.filebeans-stage', '.filebeans-vaults'}
MAX_FILES = 100_000
MAX_THUMB = 2 * 1024 * 1024
MAX_COPY = 128 * 1024 * 1024

@dataclasses.dataclass(frozen=True)
class Metadata:
    size: int
    modified_ns: int
    device: int
    inode: int

    @classmethod
    def of(cls, info):
        return cls(info.st_size, info.st_mtime_ns, info.st_dev, info.st_ino)

    @property
    def revision(self):
        return hashlib.sha256(f'{self.size}:{self.modified_ns}'.encode()).hexdigest()


def stem_key(relative):
    folder, name = posixpath.split(relative)
    # Go filepath.Ext treats a leading-dot-only name as its entire extension;
    # FileBeans keeps that whole name instead of producing an empty stem.
    index = name.rfind('.')
    stem = name[:index] if index > 0 else name
    return folder, stem


def cache_name(relative, metadata):
    data = os.fsencode(posixpath.basename(relative)) + b'\0' + metadata.revision.encode() + b'\0jpeg-320-v1'
    return hashlib.sha256(data).hexdigest() + '.jpg'


class Tree:
    def __init__(self, path):
        self.fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        self.media_identities = set()

    def close(self):
        os.close(self.fd)

    @contextlib.contextmanager
    def directory(self, parts, create_cache=False):
        fd = os.dup(self.fd)
        try:
            for part in parts:
                if not part or part in ('.', '..') or '/' in part:
                    raise ValueError('Unsafe directory component')
                if create_cache and part == CACHE:
                    try:
                        os.mkdir(part, 0o770, dir_fd=fd)
                    except FileExistsError:
                        pass
                next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                os.close(fd)
                fd = next_fd
            yield fd
        finally:
            os.close(fd)

    def metadata(self, relative):
        parts = relative.split('/')
        with self.directory(parts[:-1]) as parent:
            info = os.stat(parts[-1], dir_fd=parent, follow_symlinks=False)
            if not stat.S_ISREG(info.st_mode):
                raise ValueError('Media entry changed type')
            return Metadata.of(info)

    def inventory(self):
        result = {}
        paths = 0
        def walk(fd, prefix, depth):
            nonlocal paths
            if depth > 64:
                raise ValueError('Archive depth exceeds limit')
            with os.scandir(fd) as iterator:
                for entry in iterator:
                    if entry.name.casefold() in INTERNAL:
                        continue
                    paths += 1
                    if paths > MAX_FILES:
                        raise ValueError('Archive entry count exceeds limit')
                    info = entry.stat(follow_symlinks=False)
                    relative = prefix + entry.name
                    if stat.S_ISDIR(info.st_mode):
                        child = os.open(entry.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                        try:
                            walk(child, relative + '/', depth + 1)
                        finally:
                            os.close(child)
                    elif stat.S_ISREG(info.st_mode):
                        result[relative] = Metadata.of(info)
                    else:
                        raise ValueError('Archive contains a symlink or unsupported entry')
        walk(self.fd, '', 0)
        self.media_identities = {(m.device, m.inode) for m in result.values()}
        return result

    def read_cache(self, relative, metadata):
        parts = relative.split('/')
        with self.directory(parts[:-1] + [CACHE]) as parent:
            fd = os.open(cache_name(relative, metadata), os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW, dir_fd=parent)
            try:
                info = os.fstat(fd)
                if (info.st_dev, info.st_ino) in self.media_identities:
                    raise ValueError('Thumbnail cache aliases an original media file')
                if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_THUMB:
                    raise ValueError('Invalid thumbnail cache object')
                with os.fdopen(fd, 'rb', closefd=False) as handle:
                    return handle.read(MAX_THUMB + 1)
            finally:
                os.close(fd)

    def write_cache(self, relative, metadata, data):
        parts = relative.split('/')
        with self.directory(parts[:-1] + [CACHE], create_cache=True) as parent:
            target = cache_name(relative, metadata)
            temporary = 'import-' + uuid.uuid4().hex
            try:
                fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o660, dir_fd=parent)
                with os.fdopen(fd, 'wb') as handle:
                    handle.write(data)
                    handle.flush()
                    os.fsync(handle.fileno())
                if self.metadata(relative) != metadata:
                    raise ValueError('Archive media changed during thumbnail copy')
                os.replace(temporary, target, src_dir_fd=parent, dst_dir_fd=parent)
                os.fsync(parent)
            finally:
                try:
                    os.unlink(temporary, dir_fd=parent)
                except FileNotFoundError:
                    pass
        if hashlib.sha256(self.read_cache(relative, metadata)).digest() != hashlib.sha256(data).digest():
            raise ValueError('Copied thumbnail readback failed')


def compare(library, archive):
    def index(files):
        result = {}
        for relative in files:
            result.setdefault(stem_key(relative), []).append(relative)
        return result
    left, right = index(library), index(archive)
    ambiguous = []
    for key in sorted(left.keys() | right.keys()):
        if len(left.get(key, [])) > 1 or len(right.get(key, [])) > 1:
            ambiguous.append({'folder': key[0], 'stem': key[1], 'library': sorted(left.get(key, [])), 'archive': sorted(right.get(key, []))})
    missing_archive = sorted(p for key, paths in left.items() if key not in right for p in paths)
    missing_library = sorted(p for key, paths in right.items() if key not in left for p in paths)
    report = {'matched': not (ambiguous or missing_archive or missing_library), 'libraryFiles': len(library), 'archiveFiles': len(archive), 'missingArchive': missing_archive, 'missingLibrary': missing_library, 'ambiguous': ambiguous}
    pairs = [(left[key][0], right[key][0]) for key in sorted(left.keys() & right.keys()) if len(left[key]) == len(right[key]) == 1]
    return report, pairs


def prepare(library_path, archive_path, copy=False, progress=None):
    library, archive = Tree(library_path), Tree(archive_path)
    try:
        left, right = library.inventory(), archive.inventory()
        report, pairs = compare(left, right)
        report.update(copied=0, unchanged=0, thumbnailBytes=0, missingThumbnails=[], invalidThumbnails=[])
        if not copy or not report['matched']:
            return report
        # No cache writes until both complete trees match and the snapshots hold.
        if library.inventory() != left or archive.inventory() != right:
            raise ValueError('File tree changed during comparison')
        for number, (source, destination) in enumerate(pairs, 1):
            if library.metadata(source) != left[source] or archive.metadata(destination) != right[destination]:
                raise ValueError('Media changed since comparison')
            try:
                data = library.read_cache(source, left[source])
            except FileNotFoundError:
                report['missingThumbnails'].append(source)
                continue
            if len(data) > MAX_THUMB or not data.startswith(b'\xff\xd8\xff') or not data.endswith(b'\xff\xd9'):
                report['invalidThumbnails'].append(source)
                continue
            try:
                existing = archive.read_cache(destination, right[destination])
            except FileNotFoundError:
                existing = None
            if existing == data:
                report['unchanged'] += 1
                continue
            if report['thumbnailBytes'] + len(data) > MAX_COPY:
                raise ValueError('Thumbnail copy exceeds 128 MiB ceiling')
            if library.metadata(source) != left[source]:
                raise ValueError('Library media changed during thumbnail read')
            archive.write_cache(destination, right[destination], data)
            report['copied'] += 1
            report['thumbnailBytes'] += len(data)
            if progress and number % 25 == 0:
                progress({'processed': number, 'total': len(pairs), 'copied': report['copied'], 'thumbnailBytes': report['thumbnailBytes']})
        if library.inventory() != left or archive.inventory() != right:
            raise ValueError('File tree changed before thumbnail copy completed')
        return report
    finally:
        library.close()
        archive.close()


@contextlib.contextmanager
def protection_lease(runtime, library, archive):
    gate = os.open(runtime + '/gate', os.O_RDONLY | os.O_NOFOLLOW)
    try:
        deadline = time.monotonic() + 15
        while True:
            try:
                fcntl.flock(gate, fcntl.LOCK_SH | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise ValueError('Protected folders are busy')
                time.sleep(.1)
        with open(runtime + '/status.json') as f:
            snapshot = json.load(f)
        assert snapshot['version'] == 1
        for source, target, identity in [('VPS', library, '73b9b29e822abb8476c4ababf7de0043'), ('Samba', archive, '639f8ece321e9342de4c57ba78ddea28')]:
            vault = next(v for v in snapshot['vaults'] if v['id'] == identity)
            if vault['source'] != source or vault['path'] != '/pics' or vault['state'] != 'unlocked':
                raise ValueError('Expected protected pics folder is unavailable')
            with open('/proc/self/mountinfo') as f:
                mounted = any(line.split(' - ', 1)[0].split()[4] == target and line.split(' - ', 1)[1].split()[1] == 'filebeans-vault-' + identity for line in f)
            if not mounted:
                raise ValueError('Expected protected mount is missing')
        yield
    finally:
        os.close(gate)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--library', required=True)
    parser.add_argument('--archive', required=True)
    parser.add_argument('--protection', required=True)
    parser.add_argument('--copy', action='store_true')
    args = parser.parse_args()
    def interrupt(signum, frame):
        raise SystemExit('Thumbnail operation interrupted')
    signal.signal(signal.SIGTERM, interrupt)
    with protection_lease(args.protection, args.library, args.archive):
        result = prepare(args.library, args.archive, args.copy, lambda x: print(json.dumps({'progress': x}), flush=True))
    summary = dict(result)
    summary['missingThumbnails'] = len(result['missingThumbnails'])
    summary['invalidThumbnails'] = len(result['invalidThumbnails'])
    print(json.dumps(summary), flush=True)
    if not result['matched']:
        raise SystemExit(2)

if __name__ == '__main__':
    main()
