#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("apple_sdk", Path(__file__).with_name("check-apple-sdk.py"))
sdk = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sdk)

class AppleSDKTests(unittest.TestCase):
    def test_supported_sdks(self):
        for version in ("27", "27.0", "27.2", "27.2.1", "28.0"):
            self.assertEqual(sdk.require_current_sdk(version, "iphoneos"), version)

    def test_outdated_or_unidentified_sdk_fails_before_build(self):
        for version in ("26.3", "26.6", "18.5", "", "27 beta", "unknown", "27..2"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                sdk.require_current_sdk(version, "iphoneos")

    def test_both_workflows_check_sdk_before_building(self):
        root = Path(__file__).resolve().parents[1]
        for name in ("validate.yml", "testflight.yml"):
            workflow = (root / ".github/workflows" / name).read_text()
            self.assertIn("/Applications/Xcode_27.2.app", workflow)
            self.assertIn("python3 scripts/check-apple-sdk.py", workflow)
            self.assertNotIn("macos-15", workflow)
            self.assertLess(workflow.index("scripts/check-apple-sdk.py"), workflow.index("xcodebuild "))

if __name__ == "__main__":
    unittest.main()
