#!/usr/bin/env python3
"""Publish a reviewed npm candidate in dependency order."""

import argparse
import base64
import hashlib
import json
import re
import subprocess
import sys
import tarfile
import time
from pathlib import Path


ROOT_PACKAGE = "@synapsync/syntra-mcp"
NATIVE_PACKAGE_PREFIX = "@synapsync/syntra-"
PLATFORMS = ("darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-x64")
REGISTRY = "--registry=https://registry.npmjs.org/"
STABLE_VERSION = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"


def get_release(directory, manifest, expected_digest):
    raw = manifest.read_bytes()
    if hashlib.sha256(raw).hexdigest() != expected_digest:
        raise ValueError("reviewed manifest SHA-256 differs")
    candidate = json.loads(raw)
    version = candidate["version"]
    if candidate.get("private") is not False or not re.fullmatch(STABLE_VERSION, version):
        raise ValueError("publishing requires a public stable candidate")

    native = {f"{NATIVE_PACKAGE_PREFIX}{platform}": version for platform in PLATFORMS}
    names = [*native, ROOT_PACKAGE]
    files = [f"{name[1:].replace('/', '-')}-{version}.tgz" for name in names]
    if set(files) != set(candidate["artifacts"]) or set(files) != {file.name for file in directory.iterdir()}:
        raise ValueError("release must contain exactly the six matching tarballs")

    release = []
    for name, filename in zip(names, files):
        path = directory / filename
        if not path.is_file() or path.is_symlink():
            raise ValueError(f"invalid tarball: {filename}")
        payload = path.read_bytes()
        if hashlib.sha256(payload).hexdigest() != candidate["artifacts"][filename]:
            raise ValueError(f"tarball SHA-256 differs: {filename}")
        with tarfile.open(path, "r:gz") as archive:
            stream = archive.extractfile("package/package.json")
            if stream is None:
                raise ValueError(f"missing package manifest: {filename}")
            package = json.load(stream)
        if package["name"] != name or package["version"] != version or package.get("private") is not False:
            raise ValueError(f"unexpected package manifest: {filename}")
        if name == ROOT_PACKAGE and package.get("optionalDependencies") != native:
            raise ValueError("root package must pin the exact native release versions")
        integrity = "sha512-" + base64.b64encode(hashlib.sha512(payload).digest()).decode("ascii")
        release.append((name, path, integrity))
    return version, release


def registry_value(spec, field):
    result = subprocess.run(
        ["npm", "view", spec, field, "--json", "--prefer-online", REGISTRY],
        capture_output=True, text=True, check=False,
    )
    value = json.loads(result.stdout or "null")
    if result.returncode:
        if isinstance(value, dict) and value.get("error", {}).get("code") == "E404":
            return None
        raise ValueError(f"npm view failed for {spec}: {result.stderr.strip()}")
    if isinstance(value, list) and len(value) == 1:
        value = value[0]
    if not isinstance(value, str):
        raise ValueError(f"npm returned no {field} for {spec}")
    return value


def publish(version, release):
    published = {}
    for name, _, integrity in release:
        spec = f"{name}@{version}"
        current = registry_value(spec, "dist.integrity")
        if current is not None and current != integrity:
            raise ValueError(f"{spec} already exists with different bytes")
        published[name] = current is not None

    latest = registry_value(f"{ROOT_PACKAGE}@latest", "version")
    if latest is not None:
        if not re.fullmatch(STABLE_VERSION, latest):
            raise ValueError(f"unexpected latest tag: {latest}")
        if tuple(map(int, version.split("."))) < tuple(map(int, latest.split("."))):
            raise ValueError(f"refusing to release {version} after {latest}")

    for name, path, integrity in release:
        spec = f"{name}@{version}"
        if published[name]:
            print(f"Already verified {spec}", flush=True)
            continue
        tag = "latest" if name == ROOT_PACKAGE else f"release-{version}"
        subprocess.run(
            ["npm", "publish", str(path), "--access=public", "--ignore-scripts", "--tag", tag, REGISTRY],
            check=True,
        )
        for delay in (0, 1, 2, 4):
            time.sleep(delay)
            current = registry_value(spec, "dist.integrity")
            if current is None:
                continue
            if current != integrity:
                raise ValueError(f"{spec} registry integrity differs after publishing")
            break
        else:
            raise ValueError(f"{spec} is not yet visible after publishing")
        print(f"Verified {spec}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifacts", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("manifest_sha256")
    args = parser.parse_args()
    publish(*get_release(args.artifacts.resolve(), args.manifest, args.manifest_sha256))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, tarfile.TarError, subprocess.CalledProcessError) as error:
        print(f"publish-npm-packages: {error}", file=sys.stderr)
        sys.exit(1)
