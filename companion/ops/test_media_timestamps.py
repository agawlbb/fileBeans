import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import media_timestamps as dates


class TimestampTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'vids').mkdir()
        self.file = self.root / 'vids' / 'clip.mp4'
        self.file.write_bytes(b'encoded media')
        os.utime(self.file, ns=(123456789, 1700000000123456789))
        self.original = 1539447530123456789

    def repair(self, manifest, apply=False):
        with dates.tree(self.root) as root:
            return dates.repair(root, manifest, apply)

    def test_preview_apply_and_repeat(self):
        before = self.file.stat()
        folder = self.file.parent.stat().st_mtime_ns
        manifest = {'vids/clip.mov': self.original}
        self.assertEqual(self.repair(manifest)['wouldCorrect'], 1)
        self.assertEqual(self.file.stat().st_mtime_ns, before.st_mtime_ns)
        self.assertEqual(self.repair(manifest, True)['corrected'], 1)
        after = self.file.stat()
        self.assertEqual(after.st_mtime_ns, self.original)
        self.assertEqual(after.st_atime_ns, before.st_atime_ns)
        self.assertEqual(after.st_size, before.st_size)
        self.assertEqual(self.file.parent.stat().st_mtime_ns, folder)
        self.assertEqual(self.repair(manifest, True)['alreadyCorrect'], 1)
        self.assertEqual(self.file.read_bytes(), b'encoded media')

    def test_missing_and_ambiguous_matches(self):
        self.assertEqual(self.repair({})['unmatched'], ['vids/clip.mp4'])
        manifest = {'vids/clip.mov': self.original, 'vids/clip.avi': self.original}
        self.assertEqual(self.repair(manifest, True)['ambiguous'], ['vids/clip.mp4'])
        (self.file.parent / 'clip.jpg').write_bytes(b'other')
        self.assertEqual(len(self.repair({'vids/clip.mov': self.original}, True)['ambiguous']), 2)

    def test_hidden_symlinks_and_hardlinks(self):
        (self.root / '.filebeans-stage').mkdir()
        (self.root / '.filebeans-stage' / 'clip.mp4').write_bytes(b'private')
        (self.root / 'link').symlink_to(self.file.parent, target_is_directory=True)
        os.link(self.file, self.root / 'hardlink.mp4')
        result = self.repair({'vids/clip.mov': self.original}, True)
        self.assertEqual(result['files'], 2)
        self.assertEqual(len(result['failed']), 1)
        self.assertEqual(result['corrected'], 0)

    def test_changed_file_is_skipped(self):
        actual = dates.inventory
        def changing(root):
            result = actual(root)
            self.file.write_bytes(b'changed')
            return result
        with mock.patch.object(dates, 'inventory', side_effect=changing):
            result = self.repair({'vids/clip.mov': self.original}, True)
        self.assertEqual(len(result['failed']), 1)
        self.assertEqual(result['corrected'], 0)

    def test_unsafe_paths_and_invalid_dates(self):
        for path in ('../clip.mp4', '/clip.mp4', 'vids//clip.mov', 'vids/.hidden'):
            with self.assertRaises(ValueError):
                self.repair({path: self.original}, True)
        for value in ('2020', True, 2**64):
            with self.assertRaises(ValueError):
                self.repair({'vids/clip.mov': value}, True)


if __name__ == '__main__':
    unittest.main()
