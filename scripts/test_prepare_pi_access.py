import importlib.util
import os
from pathlib import Path
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('access', Path(__file__).with_name('prepare-pi-access.py'))
access = importlib.util.module_from_spec(spec)
spec.loader.exec_module(access)


class PiAccessTests(unittest.TestCase):
    def test_preserves_other_users_effective_access_when_widening_mask(self):
        entries = [(1, 7, access.UNDEFINED), (2, 7, 1234), (4, 7, access.UNDEFINED),
                   (8, 6, 2345), (16, 4, access.UNDEFINED), (32, 0, access.UNDEFINED)]
        raw = struct.pack('<I', 2) + b''.join(struct.pack('<HHI', *entry) for entry in entries)
        updated = list(struct.iter_unpack('<HHI', access.acl_with_user(raw, 0o770, 1000, 7)[4:]))
        result = {(tag, identity): bits for tag, bits, identity in updated}
        self.assertEqual(result[(2, 1234)], 4)
        self.assertEqual(result[(8, 2345)], 4)
        self.assertEqual(result[(4, access.UNDEFINED)], 4)
        self.assertEqual(result[(2, 1000)], 7)
        self.assertEqual(result[(32, access.UNDEFINED)], 0)

    def test_vault_stores_and_symlinks_are_untouched_and_repeat_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private = root / '.filebeans-vaults'
            private.mkdir()
            (private / 'ciphertext').write_bytes(b'sealed')
            (root / 'ordinary').write_bytes(b'original')
            (root / 'alias').symlink_to(private, target_is_directory=True)
            before = os.listxattr(private)
            visited, changed = access.prepare(root)
            self.assertEqual(visited, 2)
            self.assertGreater(changed, 0)
            self.assertEqual(os.listxattr(private), before)
            self.assertEqual((root / 'ordinary').read_bytes(), b'original')
            self.assertEqual(access.prepare(root)[1], 0)

    def test_hardlinks_do_not_grant_access_to_an_unknown_alias(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            file = root / 'file'
            file.write_bytes(b'keep')
            os.link(file, root / 'alias')
            before = os.listxattr(file)
            with self.assertRaises(ValueError):
                access.prepare(root)
            self.assertEqual(os.listxattr(file), before)

    def test_journal_keeps_original_acl_across_resume_and_rejects_other_scope(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / 'root'
            root.mkdir()
            (root / 'ordinary').write_bytes(b'keep')
            journal = access.AccessJournal(base / 'before.sqlite', root)
            try:
                access.prepare(root, journal=journal)
                first = journal.db.execute('SELECT relative,name,value FROM before_state ORDER BY id').fetchall()
                self.assertTrue(first)
                access.prepare(root, journal=journal)
                self.assertEqual(first, journal.db.execute('SELECT relative,name,value FROM before_state ORDER BY id').fetchall())
                access.restore(root, journal)
                self.assertNotIn('system.posix_acl_access', os.listxattr(root))
                self.assertNotIn('system.posix_acl_default', os.listxattr(root))
                self.assertEqual((root / 'ordinary').read_bytes(), b'keep')
            finally:
                journal.close()
            with self.assertRaises(ValueError):
                access.AccessJournal(base / 'before.sqlite', base / 'other')

    def test_recovery_never_changes_a_replacement_inode(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            root = base / 'root'
            root.mkdir()
            (root / 'ordinary').write_bytes(b'original')
            journal = access.AccessJournal(base / 'before.sqlite', root)
            try:
                access.prepare(root, journal=journal)
                (root / 'ordinary').rename(root / 'moved')
                (root / 'ordinary').write_bytes(b'replacement')
                before = os.getxattr(root / 'ordinary', 'system.posix_acl_access')
                with self.assertRaises(ValueError):
                    access.restore(root, journal)
                self.assertEqual(os.getxattr(root / 'ordinary', 'system.posix_acl_access'), before)
                self.assertEqual((root / 'ordinary').read_bytes(), b'replacement')
                self.assertEqual((root / 'moved').read_bytes(), b'original')
            finally:
                journal.close()


if __name__ == '__main__':
    unittest.main()
