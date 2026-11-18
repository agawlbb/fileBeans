#!/usr/bin/env python3
"""Copy development test tools into the isolated runtime's read-only bundle."""
import pathlib
import re
import shutil
import subprocess
import os
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
bundle = root / ".build/fuse-tests"
libraries = bundle / "ffmpeg-libs"
libraries.mkdir(parents=True, exist_ok=True)
loader = None

def install(source, target, mode=None):
    fd, temporary = tempfile.mkstemp(dir=target.parent)
    os.close(fd)
    try:
        shutil.copyfile(source, temporary)
        os.chmod(temporary, mode or 0o444)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)

for name in ["ffmpeg", "ffprobe"]:
    binary = shutil.which(name)
    if binary is None:
        raise SystemExit(f"Install {name} inside development before filesystem integration tests")
    install(binary, bundle / (name + ".bin"), 0o555)
    linked = subprocess.check_output(["ldd", binary], text=True)
    for library in re.findall(r"(/[^\s()]+)", linked):
        path = pathlib.Path(library)
        if not path.is_file():
            continue
        install(path, libraries / path.name)
        if path.name.startswith("ld-linux"):
            loader = path
    options = " -threads 1 -filter_threads 1 -filter_complex_threads 1" if name == "ffmpeg" else ""
    wrapper = bundle / name
    if wrapper.exists():
        wrapper.unlink()
    wrapper.write_text(f'#!/bin/sh\nexec /test-tools/ffmpeg-loader --library-path /test-tools/ffmpeg-libs /test-tools/{name}.bin{options} "$@"\n')
    wrapper.chmod(0o555)
if loader is None:
    raise SystemExit("FFmpeg loader was not found")
install(loader, bundle / "ffmpeg-loader", 0o555)
print("Prepared FFmpeg integration tools.")
