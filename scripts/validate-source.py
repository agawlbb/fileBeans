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
assert set(project['targets']) == {'myFiles', 'QuantumFileProvider', 'FileBeansShare', 'myFilesTests', 'myFilesUITests'}
for name, target in project['targets'].items():
    for source in target['sources']:
        source_path = source['path'] if isinstance(source, dict) else source
        assert (root / source_path).exists(), f'Missing source directory: {source_path}'
    if not name.endswith('Tests'):
        info = plistlib.loads((root / target['settings']['base']['INFOPLIST_FILE']).read_bytes())
        assert info['AppGroup'] == '$(APP_GROUP)'
        assert info['KeychainGroup'] == '$(KEYCHAIN_GROUP)'
if failures:
    print('\n'.join(failures))
    sys.exit(1)
print(f'Passed Swift syntax parsing ({len(swift_files)} files), Python parsing, plist, asset JSON, and project/workflow structure checks.')

# Drive is an app-only source, never a File Provider catalog entry or target input.
provider_sources = project['targets']['QuantumFileProvider']['sources']
assert 'App' not in provider_sources
for directory in provider_sources:
    for path in (root / directory).glob('**/*.swift'):
        text = path.read_text()
        assert 'struct DriveFile' not in text and 'struct DriveBrowser' not in text, f'Drive type leaked into provider: {path}'
        assert 'source: "drive"' not in text, f'Drive source leaked into shared catalog code: {path}'
print('Passed app-only Google Drive / File Provider source isolation checks.')

# The vault key must use an access group that the Files extension cannot read.
app_entitlements = plistlib.loads((root / project['targets']['myFiles']['settings']['base']['CODE_SIGN_ENTITLEMENTS']).read_bytes())
provider_entitlements = plistlib.loads((root / project['targets']['QuantumFileProvider']['settings']['base']['CODE_SIGN_ENTITLEMENTS']).read_bytes())
app_info = plistlib.loads((root / 'Config/App-Info.plist').read_bytes())
vault_group = app_info['VaultKeychainGroup']
assert vault_group in app_entitlements['keychain-access-groups']
assert vault_group not in provider_entitlements['keychain-access-groups']
assert '$(KEYCHAIN_GROUP)' in app_entitlements['keychain-access-groups']
assert '$(KEYCHAIN_GROUP)' in provider_entitlements['keychain-access-groups']
print('Passed biometric vault-key / File Provider Keychain isolation checks.')

share = project['targets']['FileBeansShare']
share_entitlements = plistlib.loads((root / share['settings']['base']['CODE_SIGN_ENTITLEMENTS']).read_bytes())
assert vault_group not in share_entitlements['keychain-access-groups']
assert '$(KEYCHAIN_GROUP)' in share_entitlements['keychain-access-groups']
assert 'App' not in share['sources']
assert any(dep.get('target') == 'FileBeansShare' for dep in project['targets']['myFiles']['dependencies'])
print('Passed Share extension embedding and vault-key isolation checks.')
