#!/usr/bin/env python3
"""Generate the project's geometric folder icon using only the standard library."""
from pathlib import Path
import json
import struct
import zlib

size = 1024
pixels = bytearray()
for y in range(size):
    pixels.append(0)
    for x in range(size):
        color = (20 + y // 128, 32 + y // 100, 55 + y // 80)
        # A raised folder tab, a back panel, and a lighter front panel.
        if 200 <= x < 470 and 270 <= y < 405:
            color = (66, 166, 156)
        if 200 <= x < 824 and 350 <= y < 720:
            color = (66, 166, 156)
        if 200 <= x < 824 and 410 <= y < 754:
            color = (94, 214, 187)
        # Small inset file edge suggests browsing without any lettering.
        if 592 <= x < 734 and 506 <= y < 530:
            color = (27, 103, 98)
        if 592 <= x < 700 and 555 <= y < 579:
            color = (27, 103, 98)
        pixels.extend(color)


def chunk(kind, data):
    return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data))


directory = Path(__file__).resolve().parents[1] / 'App/Assets.xcassets/AppIcon.appiconset'
directory.mkdir(parents=True, exist_ok=True)
png = b'\x89PNG\r\n\x1a\n'
png += chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 2, 0, 0, 0))
png += chunk(b'IDAT', zlib.compress(pixels, 9)) + chunk(b'IEND', b'')
(directory / 'AppIcon.png').write_bytes(png)
(directory / 'Contents.json').write_text(json.dumps({'images': [{'filename': 'AppIcon.png', 'idiom': 'universal', 'platform': 'ios', 'size': '1024x1024'}], 'info': {'author': 'xcode', 'version': 1}}, indent=2) + '\n')
(directory.parent / 'Contents.json').write_text(json.dumps({'info': {'author': 'xcode', 'version': 1}}, indent=2) + '\n')
