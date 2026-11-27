"""Grant the Pi API scoped ordinary-file access without opening vault stores."""
import argparse
import errno
import hashlib
import json
import os
from pathlib import Path
import stat
import sqlite3
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


class AccessJournal:
    """Bounded, private before-state; keep only until rollout acceptance."""
    def __init__(self, path, root, uid=1000):
        fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
                raise ValueError('Permission journal must be private and owned by the operator')
        finally:
            os.close(fd)
        self.db = sqlite3.connect(path)
        if self.db.execute('PRAGMA page_size').fetchone()[0] != 4096:
            self.close()
            raise ValueError('Unexpected permission journal page size')
        self.db.execute('PRAGMA journal_mode=DELETE')
        self.db.execute('PRAGMA max_page_count=16384')  # 64 MiB at default 4096-byte pages.
        self.db.execute('PRAGMA cache_size=-1024')
        self.db.execute('PRAGMA synchronous=FULL')
        self.db.execute('CREATE TABLE IF NOT EXISTS scope(root TEXT, uid INTEGER)')
        previous = self.db.execute('SELECT root,uid FROM scope').fetchall()
        if previous and previous != [(str(root), uid)]:
            self.close()
            raise ValueError('Permission journal belongs to a different scope')
        if not previous:
            self.db.execute('INSERT INTO scope VALUES(?,?)', (str(root), uid))
        self.db.execute('CREATE TABLE IF NOT EXISTS before_state(id INTEGER PRIMARY KEY, relative TEXT, device INTEGER, inode INTEGER, name TEXT, mode INTEGER, value BLOB, UNIQUE(relative,name))')
        self.db.commit()

    def save(self, relative, info, name, original):
        self.db.execute('INSERT OR IGNORE INTO before_state(relative,device,inode,name,mode,value) VALUES(?,?,?,?,?,?)',
                        (relative, info.st_dev, info.st_ino, name, stat.S_IMODE(info.st_mode), original))
        self.db.commit()  # Durable before the corresponding ACL write.

    def close(self):
        self.db.close()


def prepare(root, uid=1000, pace=None, journal=None):
    changed = 0
    visited = 0

    def apply(fd, depth, relative):
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
            mode = info.st_mode
            if name.endswith('_default') and original is None:
                access = read_acl(fd, 'system.posix_acl_access')
                if access:
                    entries = list(struct.iter_unpack('<HHI', access[4:]))
                    mask = next((bits for tag, bits, _ in entries if tag == MASK), 7)
                    group = next(bits for tag, bits, _ in entries if tag == GROUP_OBJ) & mask
                    mode = (mode & ~0o070) | (group << 3)
            updated = acl_with_user(original, mode, uid, permissions)
            if original != updated:
                if journal:
                    journal.save(relative, info, name, original)
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
                        apply(child, depth + 1, str(Path(relative) / entry.name))
                    finally:
                        os.close(child)

    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        apply(fd, 0, '.')
        os.fsync(fd)
    finally:
        os.close(fd)
    return visited, changed


def restore(root, journal, pace=None):
    root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    restored = 0
    try:
        for relative, device, inode, name, mode, value in journal.db.execute(
                'SELECT relative,device,inode,name,mode,value FROM before_state ORDER BY id DESC'):
            parts = Path(relative).parts
            if Path(relative).is_absolute() or '..' in parts or name not in (
                    'system.posix_acl_access', 'system.posix_acl_default'):
                raise ValueError('Invalid permission recovery record')
            fd = os.dup(root_fd)
            try:
                for index, part in enumerate(parts):
                    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
                    if index < len(parts) - 1:
                        flags |= os.O_DIRECTORY
                    child = os.open(part, flags, dir_fd=fd)
                    os.close(fd)
                    fd = child
                info = os.fstat(fd)
                if (info.st_dev, info.st_ino) != (device, inode):
                    raise ValueError('Object changed since permission preparation')
                if pace:
                    pace()
                if value is None:
                    try:
                        os.removexattr(fd, name)
                    except OSError as error:
                        if error.errno != errno.ENODATA:
                            raise
                    if name == 'system.posix_acl_access':
                        os.fchmod(fd, mode)
                else:
                    os.setxattr(fd, name, value)
                restored += 1
            finally:
                os.close(fd)
        os.fsync(root_fd)
    finally:
        os.close(root_fd)
    return restored


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, type=Path)
    parser.add_argument('--restore', action='store_true', help='Restore recorded ACLs only when inode identities still match')
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
        identity = hashlib.sha256(str(path).encode()).hexdigest()[:16]
        journal_path = Path('/share') / f'.filebeans-access-{identity}.sqlite'
        if args.restore and not journal_path.exists():
            parser.error('No permission before-state exists for this root')
        journal = AccessJournal(journal_path, path)
        try:
            if args.restore:
                restored = restore(path, journal, pace=pace)
                print(f'Permission before-state restored: {restored} ACL records')
                continue
            visited, changed = prepare(path, pace=pace, journal=journal)
        finally:
            journal.close()
        print(f'Ordinary-file access prepared: {visited} entries, {changed} ACL updates; vault stores excluded')
        print(f'Permission before-state: {journal_path}; remove after verified rollout acceptance')


if __name__ == '__main__':
    main()
