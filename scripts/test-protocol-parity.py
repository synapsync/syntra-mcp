#!/usr/bin/env python3
"""Regression checks for the source-independent protocol parity verifier."""

import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
CHECKER = "scripts/check-protocol-parity.py"
MANIFEST_REL = "reference/source-manifest.json"
TRANSITIONS_REL = "reference/contract-transitions.json"
FINGERPRINT_REL = "reference/device-fingerprint-v1.json"
CONTENT_VECTOR_REL = "core/fixtures/content-integrity-v1.json"


class ProtocolParityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="syntra-protocol-parity-")
        self.addCleanup(self.temporary.cleanup)
        self.project = pathlib.Path(self.temporary.name) / "syntra-mcp"
        self.project.mkdir()
        files = [
            "scripts/check-protocol-parity.py",
            MANIFEST_REL,
            TRANSITIONS_REL,
            FINGERPRINT_REL,
            CONTENT_VECTOR_REL,
            "core/internal/toolclient/profile.go",
        ]
        if (ROOT / "core/fixtures/content-integrity-message-anchors.json").is_file():
            files.append("core/fixtures/content-integrity-message-anchors.json")
        for relative in files:
            source = ROOT / relative
            target = self.project / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        for relative in ["reference/proto", "core/gen"]:
            shutil.copytree(ROOT / relative, self.project / relative)

    def run_check(self):
        return subprocess.run(
            [sys.executable, CHECKER],
            cwd=self.project,
            capture_output=True,
            text=True,
        )

    def assert_passes(self):
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

    def test_baseline_passes_without_git_metadata(self):
        self.assertFalse((self.project / ".git").exists())
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("syntra-mcp-wire-v1", result.stdout)

    def test_client_only_change_passes(self):
        target = self.project / "core/internal/toolclient/profile.go"
        target.write_bytes(target.read_bytes() + b"\n// editable client fixture\n")
        self.assert_passes()

    def test_each_frozen_file_and_intake_manifest_is_pinned(self):
        manifest = json.loads((ROOT / MANIFEST_REL).read_text(encoding="utf-8"))
        paths = [
            entry["destination"]
            for entry in manifest["files"]
            if entry["destination"].startswith(("reference/proto/", "core/gen/"))
            or entry["destination"] == CONTENT_VECTOR_REL
        ]
        transitions = json.loads((ROOT / TRANSITIONS_REL).read_text(encoding="utf-8"))
        active = set(paths)
        for transition in transitions["transitions"]:
            for entry in transition["files"]:
                if entry["toSha256"] is None:
                    active.discard(entry["path"])
                else:
                    active.add(entry["path"])
        paths = sorted(active)
        paths.extend([FINGERPRINT_REL, MANIFEST_REL, TRANSITIONS_REL])

        for relative in paths:
            with self.subTest(path=relative):
                target = self.project / relative
                original = target.read_bytes()
                target.write_bytes(original + b"\n")
                result = self.run_check()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(relative, result.stderr)
                target.write_bytes(original)
                self.assert_passes()

    def test_transitioned_file_chains_from_manifest_and_is_pinned(self):
        # A transition must start at the manifest digest and move to a different pinned digest.
        transitions = json.loads((ROOT / TRANSITIONS_REL).read_text(encoding="utf-8"))
        entry = transitions["transitions"][0]["files"][0]
        manifest = json.loads((ROOT / MANIFEST_REL).read_text(encoding="utf-8"))
        original = next(
            item for item in manifest["files"] if item["destination"] == entry["path"]
        )
        self.assertEqual(original["exportSha256"], entry["fromSha256"])
        self.assertNotEqual(entry["fromSha256"], entry["toSha256"])
        target = self.project / entry["path"]
        # The historical edge remains valid even if the path was later retired.
        if not target.exists():
            self.assert_passes()
            return
        target.write_bytes(target.read_bytes() + b"\n")
        result = self.run_check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(entry["path"], result.stderr)

    def test_missing_and_extra_proto_and_generated_files_fail(self):
        cases = [
            (next((self.project / root).rglob(pattern)).relative_to(self.project).as_posix(), extra)
            for root, pattern, extra in [
                ("reference/proto", "*.proto", "reference/proto/extra.proto"),
                ("core/gen", "*.go", "core/gen/extra.go"),
            ]
        ]
        for missing_relative, extra_relative in cases:
            with self.subTest(missing=missing_relative):
                missing = self.project / missing_relative
                original = missing.read_bytes()
                missing.unlink()
                result = self.run_check()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(missing_relative, result.stderr)
                missing.write_bytes(original)
                self.assert_passes()

            with self.subTest(extra=extra_relative):
                extra = self.project / extra_relative
                extra.write_text("// unreviewed fixture\n", encoding="utf-8")
                result = self.run_check()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(extra_relative, result.stderr)
                extra.unlink()
                self.assert_passes()


if __name__ == "__main__":
    unittest.main()
