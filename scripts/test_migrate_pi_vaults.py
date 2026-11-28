import argparse
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("handoff", Path(__file__).with_name("migrate-pi-vaults.py"))
handoff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(handoff)


class VaultHandoffTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / "root"
        self.state = self.base / "source-state"
        self.target = self.base / "target-state"
        self.root.mkdir()
        self.state.mkdir()
        self.identity = "a" * 32
        store = self.root / ".filebeans-vaults"
        (store / self.identity).mkdir(parents=True)
        (store / (self.identity + ".public")).mkdir()
        (store / self.identity / "gocryptfs.conf").write_text('{"fixture":true}')
        self.entry = {"id": self.identity, "source": "Samba", "device": 999, "inode": 999,
                      "parked": True, "initialized": True, "requestHash": "b" * 64,
                      "location": {"nonce": "fixture", "data": "opaque encrypted locations"}}
        self.registry = {"version": 2, "entries": [self.entry, dict(self.entry, id="c" * 32, source="VPS")]}
        (self.state / "registry.json").write_text(json.dumps(self.registry))
        (self.state / "vault-key-id").write_text("d" * 64)
        self.config = self.base / "source.json"
        self.config.write_text(json.dumps({"state": str(self.state), "roots": [{"name": "Samba", "path": str(self.root)}]}))
        self.destination = self.base / "destination.json"
        self.destination.write_text(json.dumps({"state": str(self.target), "roots": [{"name": "Samba", "path": str(self.root)}]}))
        self.bundle, self.receipt = self.base / "bundle.json", self.base / "receipt.json"

    def run_action(self, action, **extra):
        values = {"action": action, "config": str(self.config), "source": "Samba", "output": str(self.bundle),
                  "bundle": str(self.bundle), "receipt": str(self.receipt), "apply": False}
        values.update(extra)
        handoff.run(argparse.Namespace(**values))

    def test_export_retire_import_preserves_ciphertext_and_location(self):
        self.run_action("export")
        self.run_action("retire")
        self.assertEqual(json.loads((self.state / "registry.json").read_text()), self.registry)
        self.run_action("retire", apply=True)
        remaining = json.loads((self.state / "registry.json").read_text())
        self.assertEqual([e["source"] for e in remaining["entries"]], ["VPS"])
        self.run_action("import", config=str(self.destination))
        self.assertFalse((self.target / "registry.json").exists())
        self.run_action("import", config=str(self.destination), apply=True)
        imported = json.loads((self.target / "registry.json").read_text())["entries"][0]
        self.assertEqual(imported["id"], self.entry["id"])
        self.assertEqual(imported["location"], self.entry["location"])
        self.assertNotEqual(imported["device"], 999)
        self.assertEqual((self.target / "vault-key-id").read_text(), "d" * 64)
        self.run_action("import", config=str(self.destination), apply=True)
        changed = json.loads((self.target / "registry.json").read_text())
        changed["entries"][0]["inode"] += 1
        (self.target / "registry.json").write_text(json.dumps(changed))
        with self.assertRaises(ValueError):
            self.run_action("import", config=str(self.destination), apply=True)

    def test_rejects_changed_registry_unparked_and_symlinked_storage(self):
        self.run_action("export")
        (self.state / "registry.json").write_text("{}")
        with self.assertRaises(ValueError):
            self.run_action("retire", apply=True)
        entry = copy.deepcopy(self.entry)
        entry["parked"] = False
        with self.assertRaises(ValueError):
            handoff.validate_entries([entry])
        marker = self.root / ".filebeans-vaults" / (self.identity + ".public")
        marker.rmdir()
        marker.symlink_to(self.state)
        with self.assertRaises(ValueError):
            handoff.storage_identity(self.root, self.identity)


if __name__ == "__main__":
    unittest.main()
