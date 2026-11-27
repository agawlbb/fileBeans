import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("google_registration", Path(__file__).with_name("check-google-oauth.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RegistrationTest(unittest.TestCase):
    def test_native_registration_derives_google_callback_scheme(self):
        self.assertEqual(module.registration("123-native.apps.googleusercontent.com"),
                         ("123-native.apps.googleusercontent.com", "com.googleusercontent.apps.123-native"))

    def test_unconfigured_or_injected_values_cannot_reach_release_environment(self):
        for value in ("", "$(GOOGLE_IOS_CLIENT_ID)", "web.example", "123.apps.googleusercontent.com\nEVIL=value"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                module.registration(value)


if __name__ == "__main__":
    unittest.main()
