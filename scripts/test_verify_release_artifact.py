import hashlib
import importlib.util
from pathlib import Path
import plistlib
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('verify', Path(__file__).with_name('verify-release-artifact.py'))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class ReleaseArtifactTests(unittest.TestCase):
    def test_selection_rejects_ambiguous_or_unpinned_uploads(self):
        valid = dict(ARTIFACT_RUN='123', ARTIFACT_SHA256='a' * 64,
                     OPERATION='upload', EXPLICIT_BUILD_NUMBER='54')
        verify.selection(valid)
        for key, value in [('ARTIFACT_RUN', 'abc'), ('ARTIFACT_SHA256', ''),
                           ('OPERATION', 'build'), ('EXPLICIT_BUILD_NUMBER', '')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                verify.selection(dict(valid, **{key: value}))

    def test_checksum_and_application_identity_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'app.ipa'
            with zipfile.ZipFile(path, 'w') as archive:
                archive.writestr('Payload/myFiles.app/Info.plist', plistlib.dumps(
                    dict(CFBundleIdentifier='com.example.app', CFBundleVersion='54')))
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            verify.verify(path, digest, '54', 'com.example.app')
            for checksum, build, bundle in [('0' * 64, '54', 'com.example.app'),
                                            (digest, '55', 'com.example.app'),
                                            (digest, '54', 'com.other.app')]:
                with self.assertRaises(ValueError):
                    verify.verify(path, checksum, build, bundle)


if __name__ == '__main__':
    unittest.main()
