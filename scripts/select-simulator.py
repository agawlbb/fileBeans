#!/usr/bin/env python3
"""Select an available iPhone runtime compatible with the selected Xcode SDK."""
import json
import sys


def select(devices, sdk):
    sdk_version = tuple(int(x) for x in sdk.split('.')[:2])
    candidates = []
    for runtime, items in devices.items():
        if '.iOS-' not in runtime:
            continue
        version = tuple(int(x) for x in runtime.split('.iOS-')[-1].split('-')[:2])
        if version < (18, 0) or version > sdk_version:
            continue
        for item in items:
            if item.get('isAvailable', True) and item['name'].startswith('iPhone'):
                candidates.append((version, item['name'] == 'iPhone 16', item['name'], item['udid']))
    if not candidates:
        raise ValueError('No available iPhone runtime supports iOS 18 and the selected SDK; inspect installed simulators')
    return max(candidates)[3]


if __name__ == '__main__':
    print(select(json.load(sys.stdin)['devices'], sys.argv[1]))
