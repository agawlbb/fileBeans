"""Validate a retained release selection and the exact IPA before uploading."""
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import sys
import zipfile


def selection(env):
    run = env.get('ARTIFACT_RUN', '')
    digest = env.get('ARTIFACT_SHA256', '')
    if not run:
        if digest:
            raise ValueError('A checksum requires an artifact run')
        return
    if env.get('OPERATION') != 'upload':
        raise ValueError('Retained artifacts are only supported for upload')
    if not re.fullmatch(r'[1-9][0-9]*', run):
        raise ValueError('Invalid artifact run')
    if not re.fullmatch(r'[0-9a-f]{64}', digest):
        raise ValueError('A SHA-256 checksum is required')
    if not re.fullmatch(r'[1-9][0-9]*', env.get('EXPLICIT_BUILD_NUMBER', '')):
        raise ValueError('An explicit build number is required')


def verify(path, digest, build, bundle):
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise ValueError('IPA checksum mismatch')
    with zipfile.ZipFile(path) as archive:
        infos = [n for n in archive.namelist() if re.fullmatch(r'Payload/[^/]+\.app/Info.plist', n)]
        if len(infos) != 1:
            raise ValueError('Expected one application')
        info = plistlib.loads(archive.read(infos[0]))
        if info.get('CFBundleIdentifier') != bundle or str(info.get('CFBundleVersion')) != build:
            raise ValueError('IPA identity mismatch')


if __name__ == '__main__':
    if sys.argv[1:] == ['--selection']:
        selection(os.environ)
    else:
        release = json.loads((Path(__file__).resolve().parent.parent / 'Config/Release.json').read_text())
        verify(Path(sys.argv[1]), os.environ['ARTIFACT_SHA256'], os.environ['BUILD_NUMBER'], release['bundleID'])
        print('Retained IPA checksum, bundle ID and build number verified')
