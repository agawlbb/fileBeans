"""Grant the Pi API scoped ordinary-file access without opening vault stores."""
import argparse
import errno
import json
import os
from pathlib import Path
import stat
import struct
import time

USER_OBJ, USER, GROUP_OBJ, GROUP, MASK, OTHER = 1, 2, 4, 8, 16, 32
UNDEFINED = 0xFFFFFFFF


def acl_with_user(raw, mode, uid, permissions):
    if raw is None:
        entries = [(USER_OBJ, (mode >> 6) & 7, UNDEFINED),
                   (GROUP_OBJ, (mode >> 3) & 7, UNDEFINED),
                   (OTHER, mode & 7, UNDEFINED)]
    else:
        if len(raw) < 4 or (len(raw) - 4) % 8 or struct.unpack('<I', raw[:4])[0] != 2:
            raise ValueError('Unsupported filesystem ACL')
        entries = list(struct.iter_unpack('<HHI', raw[4:]))
    mask = next((bits for tag, bits, _ in entries if tag == MASK), 7)
    result = []
    for tag, bits, identity in entries:
        if tag not in (USER_OBJ, USER, GROUP_OBJ, GROUP, MASK, OTHER) or bits > 7:
            raise ValueError('Invalid filesystem ACL')
        if tag == MASK or tag == USER and identity == uid:
            continue
        # Widening the mask for our entry must not widen another user's access.
        if tag in (USER, GROUP_OBJ, GROUP):
            bits &= mask
        result.append((tag, bits, identity))
    updated_mask = permissions
    for tag, bits, _ in result:
        if tag in (USER, GROUP_OBJ, GROUP):
            updated_mask |= bits
    result.extend([(USER, permissions, uid), (MASK, updated_mask, UNDEFINED)])
    result.sort(key=lambda entry: (entry[0], entry[2]))
    return struct.pack('<I', 2) + b''.join(struct.pack('<HHI', *entry) for entry in result)


def read_acl(fd, name):
    try:
        return os.getxattr(fd, name)
    except OSError as error:
        if error.errno == errno.ENODATA:
            return None
        raise


def headroom():
    fields = dict(line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())
    if int(fields['MemAvailable'].split()[0]) * 1024 < 768 * 1024 * 1024:
        return False
    if float(Path('/proc/loadavg').read_text().split()[0]) >= 3:
        return False
    pressure = Path('/proc/pressure/io')
    if pressure.exists():
        for line in pressure.read_text().splitlines():
            if line.startswith('full '):
                values = dict(part.split('=') for part in line.split()[1:])
                if float(values['avg10']) >= 10:
                    return False
    return True


def prepare(root, uid=1000, pace=None):
    changed = 0
    visited = 0

    def apply(fd, depth):
        nonlocal changed, visited
        if depth > 64:
            raise ValueError('Permission tree exceeds depth limit')
        info = os.fstat(fd)
        directory = stat.S_ISDIR(info.st_mode)
        if not directory and not stat.S_ISREG(info.st_mode):
            return
        if not directory and info.st_nlink > 1:
            raise ValueError('Hard-linked file requires permission review')
        if pace:
            pace()
        visited += 1
        permissions = 7 if directory else 6
        names = ['system.posix_acl_access']
        if directory:
            names.append('system.posix_acl_default')
        for name in names:
            original = read_acl(fd, name)
            updated = acl_with_user(original, info.st_mode, uid, permissions)
            if original != updated:
                os.setxattr(fd, name, updated)
                changed += 1
        if directory:
            # Keep descriptors pinned; never follow a renamed symlink or read bytes.
            with os.scandir(fd) as entries:
                for entry in entries:
                    if entry.name.casefold() == '.filebeans-vaults' or entry.is_symlink():
                        continue
                    child = os.open(entry.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                                    dir_fd=fd)
                    try:
                        apply(child, depth + 1)
                    finally:
                        os.close(child)

    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        apply(fd, 0)
        os.fsync(fd)
    finally:
        os.close(fd)
    return visited, changed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, type=Path)
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    if config.get('controlUID') != 1000:
        parser.error('Expected the unprivileged Pi API identity')
    os.nice(10)

    def pace():
        while not headroom():
            time.sleep(2)
        time.sleep(.01)  # At most 100 metadata entries per second, inside app caps.

    for root in config['roots']:
        path = Path(root['path'])
        if path == Path('/share') or not path.is_relative_to('/share') or path.resolve() != path:
            parser.error('Expected a local directory below the HA share')
        visited, changed = prepare(path, pace=pace)
        print(f'Ordinary-file access prepared: {visited} entries, {changed} ACL updates; vault stores excluded')


if __name__ == '__main__':
    main()
