import importlib.machinery
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

r = importlib.machinery.SourceFileLoader('vault_roots', str(Path(__file__).with_name('filebeans-vault-roots'))).load_module()


class ParentValidation(unittest.TestCase):
    def test_empty_parent_and_previous_boot_placeholder_are_preserved(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            r.validate_samba_parent(parent)
            (parent / 'share').mkdir()
            r.validate_samba_parent(parent)
            self.assertTrue((parent / 'share').is_dir())

    def test_nonempty_placeholder_and_unknown_files_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            (parent / 'share').mkdir()
            (parent / 'share' / 'user-data').write_text('preserve')
            with self.assertRaises(RuntimeError): r.validate_samba_parent(parent)
            self.assertEqual((parent / 'share' / 'user-data').read_text(), 'preserve')
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            (parent / 'unknown').touch()
            with self.assertRaises(RuntimeError): r.validate_samba_parent(parent)

    def test_symlinks_and_existing_mounts_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp)
            (parent / 'share').symlink_to('/tmp', target_is_directory=True)
            with self.assertRaises(RuntimeError): r.validate_samba_parent(parent)
            (parent / 'share').unlink()
            (parent / 'share').mkdir()
            with patch.object(Path, 'is_mount', return_value=True):
                with self.assertRaises(RuntimeError): r.validate_samba_parent(parent)


if __name__ == '__main__': unittest.main()
