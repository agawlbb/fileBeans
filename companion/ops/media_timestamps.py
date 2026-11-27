#!/usr/bin/env python3
"""Preview or restore media modification dates from a read-only original inventory.

Inventory: --inventory ORIGINAL_ROOT
Repair: --library VPS_ROOT [--apply] [--protection RUNTIME] < inventory.json
Only regular visible files are matched, by relative directory and exact stem.
No media contents are read. Run repair inside a resource-limited container.
"""
import argparse
import collections
import contextlib
import fcntl
import json
import os
import stat
import sys
import time

MAX_FILES = 100_000
MAX_MANIFEST = 16 * 1024 * 1024


def stem_key(path):
    folder, name = os.path.split(path)
    return folder, os.path.splitext(name)[0]


@contextlib.contextmanager
def tree(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        yield fd
    finally:
        os.close(fd)


def inventory(fd):
    files = {}
    visited = 0

    def walk(parent, prefix, depth):
        nonlocal visited
        if depth > 64:
            raise ValueError('Folder depth exceeds limit')
        with os.scandir(parent) as entries:
            for entry in entries:
                if entry.name.startswith('.'):
                    continue
                visited += 1
                if visited > MAX_FILES:
                    raise ValueError('File tree exceeds limit')
                info = entry.stat(follow_symlinks=False)
                relative = prefix + entry.name
                if stat.S_ISDIR(info.st_mode):
                    with tree_at(parent, entry.name) as child:
                        walk(child, relative + '/', depth + 1)
                elif stat.S_ISREG(info.st_mode):
                    files[relative] = info
                # Symlinks and special files are never traversed or changed.
    walk(fd, '', 0)
    return files


@contextlib.contextmanager
def tree_at(parent, name):
    fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
    try:
        yield fd
    finally:
        os.close(fd)


@contextlib.contextmanager
def parent_at(root, relative):
    parts = relative.split('/')
    if any(not p or p in ('.', '..') or p.startswith('.') for p in parts):
        raise ValueError('Unsafe file path')
    with contextlib.ExitStack() as stack:
        parent = root
        for part in parts[:-1]:
            parent = stack.enter_context(tree_at(parent, part))
        yield parent, parts[-1]


def identity(info):
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns


def repair(root, dates, apply=False):
    if len(dates) > MAX_FILES:
        raise ValueError('Inventory exceeds limit')
    originals = collections.defaultdict(list)
    for path, modified in dates.items():
        if not isinstance(path, str) or path.startswith('/') or any(p in ('', '.', '..') or p.startswith('.') for p in path.split('/')):
            raise ValueError('Unsafe original path')
        if type(modified) is not int or not -(2**63) < modified < 2**63:
            raise ValueError('Invalid modification timestamp')
        originals[stem_key(path)].append(modified)
    files = inventory(root)
    outputs = collections.Counter(stem_key(p) for p in files)
    result = dict(files=len(files), matched=0, corrected=0, wouldCorrect=0, alreadyCorrect=0,
                  unmatched=[], ambiguous=[], failed=[], changes=[], earliest=None, latest=None)
    for path, before in sorted(files.items()):
        candidates = originals[stem_key(path)]
        if not candidates:
            result['unmatched'].append(path)
            continue
        if len(candidates) != 1 or outputs[stem_key(path)] != 1:
            result['ambiguous'].append(path)
            continue
        result['matched'] += 1
        modified = candidates[0]
        try:
            with parent_at(root, path) as (parent, name):
                fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
                try:
                    current = os.fstat(fd)
                    if not stat.S_ISREG(current.st_mode) or identity(current) != identity(before) or current.st_nlink != 1:
                        raise ValueError('File changed or has multiple hard links')
                    if current.st_mtime_ns == modified:
                        result['alreadyCorrect'] += 1
                    elif apply:
                        os.utime(fd, ns=(current.st_atime_ns, modified))
                        os.fsync(fd)
                        after = os.fstat(fd)
                        named = os.stat(name, dir_fd=parent, follow_symlinks=False)
                        if (after.st_dev, after.st_ino, after.st_size, after.st_atime_ns, after.st_mtime_ns) != (current.st_dev, current.st_ino, current.st_size, current.st_atime_ns, modified) or identity(named) != identity(after):
                            raise ValueError('File changed during timestamp verification')
                        result['corrected'] += 1
                    else:
                        result['wouldCorrect'] += 1
                    if current.st_mtime_ns != modified:
                        result['changes'].append(dict(path=path, size=current.st_size, old=current.st_mtime_ns, new=modified))
                    result['earliest'] = modified if result['earliest'] is None else min(result['earliest'], modified)
                    result['latest'] = modified if result['latest'] is None else max(result['latest'], modified)
                finally:
                    os.close(fd)
        except (OSError, ValueError) as error:
            result['failed'].append(dict(path=path, error=str(error)))
    return result


@contextlib.contextmanager
def protection(runtime, library):
    if not runtime:
        yield
        return
    with open(runtime + '/gate', 'rb') as gate:
        deadline = time.monotonic() + 30
        while True:
            try:
                fcntl.flock(gate, fcntl.LOCK_SH | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise ValueError('Protected storage is busy')
                time.sleep(.1)
        with open(runtime + '/status.json') as status:
            snapshot = json.load(status)
        vaults = [v for v in snapshot['vaults'] if v['source'] == 'VPS' and v['path'] == '/pics' and v['state'] == 'unlocked']
        if snapshot['version'] != 1 or len(vaults) != 1:
            raise ValueError('Protected VPS pics is unavailable')
        with open('/proc/self/mountinfo') as mounts:
            expected = 'filebeans-vault-' + vaults[0]['id']
            if not any(line.split(' - ', 1)[0].split()[4] == library and line.split(' - ', 1)[1].split()[1] == expected for line in mounts):
                raise ValueError('Expected protected library mount is missing')
        yield


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument('--inventory')
    modes.add_argument('--library')
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--protection')
    args = parser.parse_args()
    if args.inventory:
        with tree(args.inventory) as root:
            print(json.dumps({path: s.st_mtime_ns for path, s in inventory(root).items()}))
        return
    data = sys.stdin.buffer.read(MAX_MANIFEST + 1)
    if len(data) > MAX_MANIFEST:
        raise ValueError('Inventory exceeds size limit')
    dates = json.loads(data)
    with protection(args.protection, args.library), tree(args.library) as root:
        result = repair(root, dates, args.apply)
    print(json.dumps(result))
    if result['failed']:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
