#!/usr/bin/env python3
"""Static source/config checks; does not compile Apple SDK code or contact servers."""
from pathlib import Path
import ast
import json
import plistlib
import sys
import tree_sitter
import tree_sitter_swift
import yaml

root = Path(__file__).resolve().parents[1]
parser = tree_sitter.Parser(tree_sitter.Language(tree_sitter_swift.language()))
failures = []
swift_files = sorted(root.glob('**/*.swift'))
for path in swift_files:
    tree = parser.parse(path.read_bytes())
    if tree.root_node.has_error:
        pending = [tree.root_node]
        while pending:
            node = pending.pop()
            if node.type == 'ERROR' or node.is_missing:
                failures.append(f'{path.relative_to(root)}:{node.start_point.row + 1}: Swift parse error: {node.text[:100]!r}')
            pending.extend(node.children)
for path in root.glob('scripts/*.py'):
    ast.parse(path.read_text(), filename=str(path))
for path in list(root.glob('Config/*.plist')) + list(root.glob('Config/*.entitlements')) + list(root.glob('Shared/*.xcprivacy')):
    plistlib.loads(path.read_bytes())
for path in root.glob('**/Contents.json'):
    json.loads(path.read_text())
release = json.loads((root / 'Config/Release.json').read_text())
assert release['appGroup'] == 'group.' + release['bundleID']
configuration = (root / 'Config/Project.xcconfig').read_text()
assert f"BUNDLE_ID_PREFIX = {release['bundleID']}" in configuration
assert f"APPLE_TEAM_ID = {release['teamID']}" in configuration
project = yaml.safe_load((root / 'project.yml').read_text())
for path in root.glob('.github/workflows/*.yml'):
    workflow = yaml.safe_load(path.read_text())
    assert workflow['permissions'] == {'contents': 'read'}, f'Unexpected workflow permissions: {path}'
    assert workflow.get('on', workflow.get(True)), f'Missing workflow triggers: {path}'
assert set(project['targets']) == {'myFiles', 'QuantumFileProvider', 'myFilesTests'}
for name, target in project['targets'].items():
    for source in target['sources']:
        assert (root / source).exists(), f'Missing source directory: {source}'
    if name != 'myFilesTests':
        info = plistlib.loads((root / target['settings']['base']['INFOPLIST_FILE']).read_bytes())
        assert info['AppGroup'] == '$(APP_GROUP)'
        assert info['KeychainGroup'] == '$(KEYCHAIN_GROUP)'
if failures:
    print('\n'.join(failures))
    sys.exit(1)
print(f'Passed Swift syntax parsing ({len(swift_files)} files), Python parsing, plist, asset JSON, and project/workflow structure checks.')
