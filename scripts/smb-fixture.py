#!/usr/bin/env python3
"""Synthetic loopback-only SMB2 server for the isolated filesystem tests."""
import argparse
import logging
import sys
from importlib.metadata import version

from impacket import smbserver
from impacket.ntlm import compute_lmhash, compute_nthash

parser = argparse.ArgumentParser()
parser.add_argument("--root", required=True)
parser.add_argument("--port", type=int, default=1445)
args = parser.parse_args()
if version("impacket") != "0.13.0":
    raise SystemExit("Install the pinned SMB test dependency")
server = smbserver.SimpleSMBServer(listenAddress="127.0.0.1", listenPort=args.port)
server.addShare("vaults", args.root, "Synthetic ciphertext storage", readOnly="no")
server.setSMB2Support(True)
server.addCredential("fixture", 0, compute_lmhash("fixture-only"), compute_nthash("fixture-only"))
server.setLogFile("None")
logging.basicConfig(stream=sys.stderr, level=logging.INFO, force=True)
server.start()
