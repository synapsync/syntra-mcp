#!/usr/bin/env python3
"""Check frozen protocol bytes against the reviewed standalone handoff."""

import hashlib
import json
import pathlib
import re
import struct
import sys
import uuid


ROOT = pathlib.Path(__file__).resolve().parents[1]
BASELINE_ID = "syntra-mcp-wire-v1"
MANIFEST_REL = "reference/source-manifest.json"
MANIFEST_SHA256 = "4fc48cd2af56cccd5526ad3279a95b33e1ac4c4feba16aa2e9b43d1eeb2472fa"
TRANSITIONS_REL = "reference/contract-transitions.json"
TRANSITIONS_SHA256 = "265db431f3f4ed68443af1e7158d1ccdbc2ec6a4f6eb234d4408d6b358b77ae5"
FINGERPRINT_REL = "reference/device-fingerprint-v1.json"
FINGERPRINT_SHA256 = "d47c6d43307122886e60300607ffb5f4109199837c00a300143df6378f0b104e"
CONTENT_VECTOR_REL = "core/fixtures/content-integrity-v1.json"


class ParityError(Exception):
    """A frozen baseline or vector check failed."""


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def checked_file(relative):
    path = ROOT / relative
    if path.is_symlink() or not path.is_file():
        raise ParityError(f"{relative}: missing or not a regular file")
    if not path.resolve().is_relative_to(ROOT):
        raise ParityError(f"{relative}: resolves outside the repository")
    return path


def read_json(relative):
    path = checked_file(relative)
    try:
        return json.loads(path.read_bytes())
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ParityError(f"{relative}: invalid JSON: {error}") from error


def check_file_digest(relative, expected):
    path = checked_file(relative)
    actual = sha256(path.read_bytes())
    if actual != expected:
        raise ParityError(
            f"{relative}: SHA-256 mismatch; expected {expected}, got {actual}"
        )


def inventory(root_relative):
    directory = ROOT / root_relative
    if not directory.is_dir() or directory.is_symlink():
        raise ParityError(f"{root_relative}: missing or not a regular directory")
    return {
        path.relative_to(ROOT).as_posix()
        for path in directory.rglob("*")
        if path.is_file() or path.is_symlink()
    }


def check_inventory(root_relative, expected, suffix=None):
    if suffix is not None:
        expected = {path for path in expected if path.endswith(suffix)}
    actual = inventory(root_relative)
    if suffix is not None:
        actual = {path for path in actual if path.endswith(suffix)}
    missing = sorted(expected - actual)
    extra = sorted(actual - expected)
    if missing:
        raise ParityError(f"{missing[0]}: missing frozen wire file")
    if extra:
        raise ParityError(f"{extra[0]}: unexpected frozen wire file")
    return len(expected)


def expected_digests():
    manifest_path = checked_file(MANIFEST_REL)
    manifest_bytes = manifest_path.read_bytes()
    actual_manifest_digest = sha256(manifest_bytes)
    if actual_manifest_digest != MANIFEST_SHA256:
        raise ParityError(
            f"{MANIFEST_REL}: SHA-256 mismatch; expected {MANIFEST_SHA256}, "
            f"got {actual_manifest_digest}"
        )
    try:
        manifest = json.loads(manifest_bytes)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ParityError(f"{MANIFEST_REL}: invalid JSON: {error}") from error
    if not isinstance(manifest, dict) or not isinstance(manifest.get("files"), list):
        raise ParityError(f"{MANIFEST_REL}: expected an object with a files array")

    digests = {}
    for entry in manifest["files"]:
        if not isinstance(entry, dict):
            raise ParityError(f"{MANIFEST_REL}: malformed file entry")
        relative = entry.get("destination")
        digest = entry.get("exportSha256")
        if not isinstance(relative, str) or not isinstance(digest, str):
            raise ParityError(f"{MANIFEST_REL}: file entry lacks destination/exportSha256")
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ParityError(f"{MANIFEST_REL}: invalid export digest for {relative}")
        if relative.startswith(("reference/proto/", "core/gen/")) or relative == CONTENT_VECTOR_REL:
            if relative in digests:
                raise ParityError(f"{MANIFEST_REL}: duplicate frozen path {relative}")
            digests[relative] = digest

    apply_transitions(digests)
    if "currentFiles" in manifest:
        current = {entry["destination"]: entry["exportSha256"] for entry in manifest["currentFiles"]}
        if len(current) != len(manifest["currentFiles"]) or current != digests:
            raise ParityError(f"{MANIFEST_REL}: current inventory does not match the transitions")
    proto_paths = {path for path in digests if path.startswith("reference/proto/")}
    generated_paths = {path for path in digests if path.startswith("core/gen/")}
    if not proto_paths or not generated_paths or CONTENT_VECTOR_REL not in digests:
        raise ParityError(f"{MANIFEST_REL}: frozen protocol inventory is incomplete")
    return digests, proto_paths, generated_paths


def apply_transitions(digests):
    """Apply reviewed post-handoff contract transitions on top of the pinned manifest.

    The original manifest stays authoritative for the handoff bytes. Every
    transition must start from the digest currently expected for that file, so the
    chain handoff -> reviewed update is explicit and cannot skip or fork.
    """
    path = checked_file(TRANSITIONS_REL)
    raw = path.read_bytes()
    actual = sha256(raw)
    if actual != TRANSITIONS_SHA256:
        raise ParityError(
            f"{TRANSITIONS_REL}: SHA-256 mismatch; expected {TRANSITIONS_SHA256}, got {actual}"
        )
    try:
        document = json.loads(raw)
        transitions = document["transitions"]
        if document.get("baseline") != BASELINE_ID or not isinstance(transitions, list):
            raise ValueError("unexpected wire baseline or transitions")
        for transition in transitions:
            for entry in transition["files"]:
                relative, old, new = entry["path"], entry["fromSha256"], entry["toSha256"]
                if relative not in digests and (old is not None or (relative != "core/fixtures/content-integrity-message-anchors.json" and not relative.startswith(("reference/proto/", "core/gen/")))):
                    raise ParityError(f"{TRANSITIONS_REL}: {relative} is not a valid new wire path")
                if new is None:
                    if relative == CONTENT_VECTOR_REL or relative not in digests or digests[relative] != old:
                        raise ParityError(f"{TRANSITIONS_REL}: invalid removal for {relative}")
                    del digests[relative]
                    continue
                if not re.fullmatch(r"[0-9a-f]{64}", new):
                    raise ParityError(f"{TRANSITIONS_REL}: invalid digest for {relative}")
                if digests.get(relative) != old:
                    raise ParityError(
                        f"{TRANSITIONS_REL}: {relative} transition does not start from the expected digest"
                    )
                digests[relative] = new
    except (KeyError, TypeError, ValueError) as error:
        raise ParityError(f"{TRANSITIONS_REL}: invalid transitions: {error}") from error


def check_fingerprint_vector():
    relative = FINGERPRINT_REL
    vector = read_json(relative)
    try:
        encoded = (
            bytes.fromhex(vector["domainHex"])
            + uuid.UUID(vector["requestId"]).bytes
            + bytes.fromhex(vector["secretDigestHex"])
            + bytes.fromhex(vector["proofDigestHex"])
        )
        label = vector["label"].encode("utf-8")
        encoded += struct.pack(">I", len(label)) + label
        encoded += struct.pack(">iI", vector["expiresInSeconds"], len(vector["capabilities"]))
        for capability in vector["capabilities"]:
            encoded += struct.pack(">i", capability)
    except (KeyError, TypeError, ValueError, struct.error) as error:
        raise ParityError(f"{relative}: invalid fingerprint vector: {error}") from error

    actual = sha256(encoded)
    if actual != vector.get("fingerprintHex"):
        raise ParityError(
            f"{relative}: fingerprint mismatch; expected {vector.get('fingerprintHex')}, got {actual}"
        )


def check_content_vectors(relative=CONTENT_VECTOR_REL):
    vectors = read_json(relative)
    if not isinstance(vectors, list):
        raise ParityError(f"{relative}: expected an array of vectors")
    for index, item in enumerate(vectors):
        try:
            actual = sha256(bytes.fromhex(item["encodingHex"]))
            expected = item["sha256"]
        except (KeyError, TypeError, ValueError) as error:
            raise ParityError(f"{relative}: invalid vector at index {index}: {error}") from error
        if actual != expected:
            raise ParityError(
                f"{relative}: vector {index} digest mismatch; expected {expected}, got {actual}"
            )


def check():
    digests, proto_paths, generated_paths = expected_digests()
    proto_count = check_inventory("reference/proto", proto_paths, ".proto")
    generated_count = check_inventory("core/gen", generated_paths)

    for relative, expected in digests.items():
        check_file_digest(relative, expected)
    check_file_digest(FINGERPRINT_REL, FINGERPRINT_SHA256)
    check_fingerprint_vector()
    check_content_vectors()
    if "core/fixtures/content-integrity-message-anchors.json" in digests:
        check_content_vectors("core/fixtures/content-integrity-message-anchors.json")
    return proto_count, generated_count


def main():
    try:
        proto_count, generated_count = check()
    except ParityError as error:
        print(f"Protocol parity failed: {error}", file=sys.stderr)
        return 1

    print(
        "Protocol parity passed: "
        f"baseline {BASELINE_ID}; {proto_count} Protobuf files, "
        f"{generated_count} generated wire files, 2 vector files, "
        f"intake manifest SHA-256 {MANIFEST_SHA256}, "
        f"reviewed transitions SHA-256 {TRANSITIONS_SHA256}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
