#!/usr/bin/env python3
"""Exercise registry propagation without credentials or network requests."""

from pathlib import Path
import runpy
import unittest
from unittest.mock import patch


PUBLISHER = Path(__file__).with_name("publish-npm-packages.py")
VERSION = "1.2.3"
NATIVE = ("@synapsync/syntra-darwin-arm64", "@synapsync/syntra-linux-x64")
LAUNCHER = "@synapsync/syntra-mcp"
RELEASE = [(name, Path(name), f"sha512-{name}") for name in (*NATIVE, LAUNCHER)]


class RegistryPropagationTest(unittest.TestCase):
    def test_delayed_native_visibility_keeps_launcher_unpublished(self):
        publish = runpy.run_path(str(PUBLISHER))["publish"]
        submitted = []
        polls = {}
        visible = set()

        def submit(argv, **options):
            name = str(argv[2])
            if name == LAUNCHER:
                self.assertEqual(visible, set(NATIVE))
            submitted.append(name)

        def registry_value(spec, field):
            if spec.endswith("@latest"):
                return "1.2.2"
            name = spec.removesuffix(f"@{VERSION}")
            if name not in submitted:
                return None
            polls[name] = polls.get(name, 0) + 1
            if polls[name] < 3:
                return None
            self.assertTrue(set(NATIVE).issubset(submitted))
            visible.add(name)
            return f"sha512-{name}"

        with patch.dict(publish.__globals__, {"registry_value": registry_value}), \
             patch("subprocess.run", side_effect=submit), patch("time.sleep"):
            publish(VERSION, RELEASE)

        self.assertEqual(submitted, [*NATIVE, LAUNCHER])
        self.assertEqual(visible, {*NATIVE, LAUNCHER})


if __name__ == "__main__":
    unittest.main()
