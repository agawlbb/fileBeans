#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('simulator', Path(__file__).with_name('select-simulator.py'))
simulator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(simulator)


class SimulatorSelectionTests(unittest.TestCase):
    def test_prefers_current_sdk_over_first_older_runtime(self):
        devices = {
            'com.apple.CoreSimulator.SimRuntime.iOS-18-5': [{'name': 'iPhone 16', 'udid': 'old'}],
            'com.apple.CoreSimulator.SimRuntime.iOS-26-2': [{'name': 'iPhone 16', 'udid': 'current'}],
            'com.apple.CoreSimulator.SimRuntime.iOS-27-0': [{'name': 'iPhone 18', 'udid': 'too-new'}],
        }
        self.assertEqual(simulator.select(devices, '26.2'), 'current')

    def test_ignores_unavailable_and_non_iphone_devices(self):
        devices = {'com.apple.CoreSimulator.SimRuntime.iOS-26-2': [
            {'name': 'iPhone 16', 'udid': 'unavailable', 'isAvailable': False},
            {'name': 'iPad Pro', 'udid': 'ipad'},
            {'name': 'iPhone 17', 'udid': 'available'},
        ]}
        self.assertEqual(simulator.select(devices, '26.2'), 'available')

    def test_rejects_runtime_older_than_app_minimum(self):
        with self.assertRaises(ValueError):
            simulator.select({'com.apple.CoreSimulator.SimRuntime.iOS-17-5': [
                {'name': 'iPhone 15', 'udid': 'old'}]}, '26.2')


if __name__ == '__main__':
    unittest.main()
