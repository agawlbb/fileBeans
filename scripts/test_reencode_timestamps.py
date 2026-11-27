import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('reencode', Path(__file__).with_name('reencode-library.py'))
reencode = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reencode)


class ReencodeTimestampTests(unittest.TestCase):
    def test_drive_date_precision_and_timezone(self):
        self.assertEqual(reencode.modified_ns('2018-10-13T16:18:50.123456789Z'), 1539447530123456789)
        self.assertEqual(reencode.modified_ns('2018-10-13T17:18:50.123456789+01:00'), 1539447530123456789)
        with self.assertRaises(ValueError):
            reencode.modified_ns('2018-10-13T16:18:50')

    def test_delivery_and_retry_preserve_source_date(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            worker = reencode.Worker.__new__(reencode.Worker)
            worker.role = 'vps'
            worker.root = root
            worker.config = {'destination': str(root / 'library')}
            (root / 'library').mkdir()
            date = 1539447530123456789
            payload = b'validated encoding'
            row = {'source_modified_ns': date, 'output_sha256': hashlib.sha256(payload).hexdigest()}
            worker.state = {'jobs': {'test': row}}
            worker.save = mock.Mock()
            worker.stop = mock.Mock(is_set=lambda: False)
            job = {'id': 'test', 'output_relative': 'clip.mp4'}
            for attempt in range(2):
                output = root / 'test.mp4'
                output.write_bytes(payload)
                with mock.patch.object(reencode.os, 'chown'):
                    worker.deliver(job, output)
                self.assertEqual(row['phase'], 'completed')
                self.assertFalse(output.exists())
                self.assertEqual((root / 'library' / 'clip.mp4').stat().st_mtime_ns, date)
            self.assertEqual((root / 'library' / 'clip.mp4').read_bytes(), payload)

    def test_cached_input_recovers_original_date(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'original').mkdir()
            source = root / 'original' / 'clip.mov'
            source.write_bytes(b'original')
            date = 1539447530123456789
            reencode.os.utime(source, ns=(date, date))
            (root / 'test.input').write_bytes(b'original')
            worker = reencode.Worker.__new__(reencode.Worker)
            worker.root = root
            worker.role = 'vm'
            worker.config = {'source_mount': str(source.parent)}
            worker.state = {'jobs': {'test': {}}}
            worker.save = mock.Mock()
            job = {'id': 'test', 'relative_path': 'clip.mov', 'source_size': 8,
                   'source_md5': hashlib.md5(b'original').hexdigest()}
            with mock.patch.object(Path, 'is_mount', return_value=True):
                self.assertEqual(worker.prepare(job), root / 'test.input')
            self.assertEqual(worker.row(job)['source_modified_ns'], date)

    def test_delivery_does_not_stamp_a_symlink_target(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / 'test.mp4'
            output.write_bytes(b'validated encoding')
            target = root / 'untouched.mp4'
            target.write_bytes(output.read_bytes())
            before = target.stat().st_mtime_ns
            (root / 'library').mkdir()
            (root / 'library' / 'clip.mp4').symlink_to(target)
            worker = reencode.Worker.__new__(reencode.Worker)
            worker.role, worker.root = 'vps', root
            worker.config = {'destination': str(root / 'library')}
            row = {'source_modified_ns': 1539447530123456789,
                   'output_sha256': hashlib.sha256(output.read_bytes()).hexdigest()}
            worker.state = {'jobs': {'test': row}}
            worker.save = mock.Mock()
            worker.stop = mock.Mock(is_set=lambda: False)
            worker.deliver({'id': 'test', 'output_relative': 'clip.mp4'}, output)
            self.assertEqual(row['phase'], 'failed')
            self.assertEqual(target.stat().st_mtime_ns, before)
            self.assertTrue(output.exists())


if __name__ == '__main__':
    unittest.main()
