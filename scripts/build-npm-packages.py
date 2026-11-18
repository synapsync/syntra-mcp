#!/usr/bin/env python3
"""Build and validate same-version Syntra npm tarballs offline."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path, PurePosixPath
from typing import Any, Optional


ROOT = Path(__file__).resolve().parents[1]
CORE = ROOT / "core"
VENDOR_FILE_HASHES = {
    "bin/vendor/jsonc-parser/LICENSE.md": "c3a24c02d678e5f0711623dcf3ab3f243b273c2633f638a1fa8e35fb7c7f8e4d",
    "bin/vendor/jsonc-parser/lib/umd/main.js": "6dc132e2e792a9616688ff93d10dd671c1fbcfa3301d76bff3e81193d049337d",
    "bin/vendor/jsonc-parser/lib/umd/impl/edit.js": "9ecd115039cf7a478f223e4a9649170c7f5e8fd8016df1b267723b57553f9483",
    "bin/vendor/jsonc-parser/lib/umd/impl/format.js": "f9e468e436bf1014a4395b1ac70374edb918739ba90b19e6e6a22ca0214564a9",
    "bin/vendor/jsonc-parser/lib/umd/impl/parser.js": "a35db1bfb9631dcba643fa42972e9ebe2a318b4de3cac239d29081577a43d688",
    "bin/vendor/jsonc-parser/lib/umd/impl/scanner.js": "581d82c4f1aa84b0605369c01f414fb0164ee462e03daa298cb960076a2a9e82",
    "bin/vendor/jsonc-parser/lib/umd/impl/string-intern.js": "e590d27909a7c132382fbf9b8c4668054ab425734c1e0789753063356826c30f",
    "bin/vendor/smol-toml/LICENSE": "fa5659948374d4f555594f47f6da073b40dc503e921aeeece30df4362b3051a5",
    "bin/vendor/smol-toml/index.cjs": "f9029370259e9a43a2f3e64fa1d39e72f3598188380ace83a0abb8261fa33555",
}
VENDOR_METADATA = {
    "format": 1,
    "dependencies": [
        {
            "name": "jsonc-parser",
            "version": "3.3.1",
            "source": "https://registry.npmjs.org/jsonc-parser/-/jsonc-parser-3.3.1.tgz",
            "integrity": "sha512-HUgH65KyejrUFPvHFPbqOY0rsFip3Bo5wb4ngvdi1EpCYWUQDC5V+Y7mZws+DLkr4M//zQJoanu1SP+87Dv1oQ==",
            "license": "MIT",
            "files": {
                "jsonc-parser/LICENSE.md": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/LICENSE.md"],
                "jsonc-parser/lib/umd/main.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/main.js"],
                "jsonc-parser/lib/umd/impl/edit.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/impl/edit.js"],
                "jsonc-parser/lib/umd/impl/format.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/impl/format.js"],
                "jsonc-parser/lib/umd/impl/parser.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/impl/parser.js"],
                "jsonc-parser/lib/umd/impl/scanner.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/impl/scanner.js"],
                "jsonc-parser/lib/umd/impl/string-intern.js": VENDOR_FILE_HASHES["bin/vendor/jsonc-parser/lib/umd/impl/string-intern.js"],
            },
        },
        {
            "name": "smol-toml",
            "version": "1.9.1",
            "source": "https://registry.npmjs.org/smol-toml/-/smol-toml-1.9.1.tgz",
            "integrity": "sha512-j7WyPdf0CJoutMzMoPLF/bVXHtKZIT9Rb4eGeEbWw8X2t5oirjPgpL+OpaENZrILYaXqYwnVH+qgUwVbl+pUJQ==",
            "license": "BSD-3-Clause",
            "files": {
                "smol-toml/LICENSE": VENDOR_FILE_HASHES["bin/vendor/smol-toml/LICENSE"],
                "smol-toml/index.cjs": VENDOR_FILE_HASHES["bin/vendor/smol-toml/index.cjs"],
            },
        },
    ],
}
ROOT_FILES = (
    "README.md",
    "bin/launcher.cjs",
    "bin/setup.cjs",
    "bin/vendor/dependencies.json",
    *VENDOR_FILE_HASHES.keys(),
    "skills/syntra/SKILL.md",
    "skills/syntra/references/tools.md",
)
LIFECYCLE_HOOKS = {
    "preinstall",
    "install",
    "postinstall",
    "prepublish",
    "prepublishOnly",
    "prepare",
    "prepack",
    "postpack",
}
TARGETS = (
    {
        "directory": "linux-x64",
        "package": "@synapsync/syntra-linux-x64",
        "os": "linux",
        "cpu": "x64",
        "goos": "linux",
        "goarch": "amd64",
        "executable": "bin/syntra",
    },
    {
        "directory": "linux-arm64",
        "package": "@synapsync/syntra-linux-arm64",
        "os": "linux",
        "cpu": "arm64",
        "goos": "linux",
        "goarch": "arm64",
        "executable": "bin/syntra",
    },
    {
        "directory": "darwin-x64",
        "package": "@synapsync/syntra-darwin-x64",
        "os": "darwin",
        "cpu": "x64",
        "goos": "darwin",
        "goarch": "amd64",
        "executable": "bin/syntra",
    },
    {
        "directory": "darwin-arm64",
        "package": "@synapsync/syntra-darwin-arm64",
        "os": "darwin",
        "cpu": "arm64",
        "goos": "darwin",
        "goarch": "arm64",
        "executable": "bin/syntra",
    },
    {
        "directory": "win32-x64",
        "package": "@synapsync/syntra-win32-x64",
        "os": "win32",
        "cpu": "x64",
        "goos": "windows",
        "goarch": "amd64",
        "executable": "bin/syntra.exe",
    },
)


class BuildError(RuntimeError):
    pass


def fail(message: str) -> None:
    raise BuildError(message)


def read_manifest(path: Path) -> tuple[dict[str, Any], bytes]:
    try:
        raw = path.read_bytes()
        value = json.loads(raw)
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read JSON manifest {path}: {error}")
    if not isinstance(value, dict):
        fail(f"manifest must contain a JSON object: {path}")
    return value, raw


def lifecycle_check(manifest: dict[str, Any], label: str) -> None:
    scripts = manifest.get("scripts", {})
    if not isinstance(scripts, dict):
        fail(f"{label} scripts must be an object")
    hooks = sorted(LIFECYCLE_HOOKS.intersection(scripts))
    if hooks:
        fail(f"{label} must not define npm lifecycle hooks: {', '.join(hooks)}")


def validate_vendor_inventory() -> None:
    metadata_path = ROOT / "bin/vendor/dependencies.json"
    metadata, _ = read_manifest(metadata_path)
    if metadata != VENDOR_METADATA:
        fail("vendored parser versions, source integrity, licenses, or file digests changed")

    vendor_root = ROOT / "bin/vendor"
    actual_files = sorted(
        path.relative_to(ROOT).as_posix()
        for path in vendor_root.rglob("*")
        if path.is_file() or path.is_symlink()
    )
    expected_files = sorted(["bin/vendor/dependencies.json", *VENDOR_FILE_HASHES])
    if actual_files != expected_files:
        fail("vendored parser inventory changed; review provenance and update the package allowlist")

    for relative, expected_hash in VENDOR_FILE_HASHES.items():
        path = ROOT / relative
        if path.is_symlink() or not path.is_file():
            fail(f"vendored parser file is missing or unsafe: {relative}")
        actual_hash = hashlib.sha256(path.read_bytes()).hexdigest()
        if actual_hash != expected_hash:
            fail(f"vendored parser file digest mismatch: {relative}")

def validate_manifests() -> tuple[dict[str, Any], dict[Path, bytes]]:
    root_path = ROOT / "package.json"
    root_manifest, root_bytes = read_manifest(root_path)
    version = root_manifest.get("version")
    if root_manifest.get("name") != "@synapsync/syntra-mcp" or root_manifest.get("private") is not True:
        fail("root manifest must be the private @synapsync/syntra-mcp package")
    if not isinstance(version, str) or not version:
        fail("root package version must be a non-empty string")
    if root_manifest.get("engines", {}).get("node") != ">=22.12":
        fail("root manifest must require Node >=22.12")
    if root_manifest.get("bin") != {"syntra": "bin/launcher.cjs"}:
        fail("root manifest bin must point only to bin/launcher.cjs")
    root_files = root_manifest.get("files")
    if not isinstance(root_files, list) or set(root_files) != {
        "README.md", "bin/launcher.cjs", "bin/setup.cjs", "bin/vendor/**", "skills/syntra/**"
    }:
        fail("root manifest files must include only README, the launcher/setup adapters, vendored parsers, and skill")
    optional = {target["package"]: version for target in TARGETS}
    if root_manifest.get("optionalDependencies") != optional:
        fail("root optionalDependencies must be exact same-version pins for all five targets")
    for field in ("dependencies", "devDependencies", "peerDependencies"):
        if root_manifest.get(field):
            fail(f"root package must not add {field}; only native optional packages are permitted")
    lifecycle_check(root_manifest, "root package")

    source_manifests: dict[Path, bytes] = {root_path: root_bytes}
    for relative in ("package.json", *ROOT_FILES):
        path = ROOT / relative
        if not path.is_file() or path.is_symlink():
            fail(f"required root package file is missing or unsafe: {relative}")

    validate_vendor_inventory()

    actual_skill_files = sorted(
        path.relative_to(ROOT).as_posix()
        for path in (ROOT / "skills/syntra").rglob("*")
        if path.is_file() and not path.is_symlink()
    )
    expected_skill_files = sorted(path for path in ROOT_FILES if path.startswith("skills/"))
    if actual_skill_files != expected_skill_files:
        fail("bundled skill inventory changed; review and update the package inventory before building")

    launcher_mode = stat.S_IMODE((ROOT / "bin/launcher.cjs").stat().st_mode)
    if launcher_mode != 0o755:
        fail(f"launcher mode must be 0755, got {launcher_mode:04o}")

    for target in TARGETS:
        manifest_path = ROOT / "packages" / target["directory"] / "package.json"
        manifest, raw = read_manifest(manifest_path)
        if manifest.get("name") != target["package"] or manifest.get("version") != version:
            fail(f"platform manifest name/version mismatch: packages/{target['directory']}/package.json")
        if manifest.get("private") is not True:
            fail(f"platform manifest must remain private: {target['package']}")
        if manifest.get("os") != [target["os"]] or manifest.get("cpu") != [target["cpu"]]:
            fail(f"platform manifest target tuple mismatch: {target['package']}")
        if manifest.get("files") != [target["executable"]]:
            fail(f"platform manifest must include only {target['executable']}: {target['package']}")
        for field in ("dependencies", "devDependencies", "optionalDependencies", "peerDependencies"):
            if manifest.get(field):
                fail(f"platform package must not add {field}: {target['package']}")
        lifecycle_check(manifest, target["package"])
        source_manifests[manifest_path] = raw

    return root_manifest, source_manifests


def executable_path(command: Optional[str], name: str) -> str:
    resolved = command or shutil.which(name)
    if not resolved:
        fail(f"required command not found: {name}")
    return str(Path(resolved).resolve())


def command_version(command: str, args: list[str], env: Optional[dict[str, str]] = None) -> str:
    result = subprocess.run([command, *args], env=env, check=False, capture_output=True, text=True)
    if result.returncode != 0:
        fail(f"version check failed for {command}: {result.stderr.strip()}")
    return (result.stdout or result.stderr).strip()


def base_tool_environment(temp_root: Path) -> dict[str, str]:
    home = temp_root / "npm-home"
    tmp = temp_root / "tool-tmp"
    home.mkdir(parents=True, exist_ok=True)
    tmp.mkdir(parents=True, exist_ok=True)
    env = {
        "PATH": os.environ.get("PATH", os.defpath),
        "HOME": str(home),
        "USERPROFILE": str(home),
        "TMPDIR": str(tmp),
        "TEMP": str(tmp),
        "TMP": str(tmp),
    }
    for key in ("SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"):
        if key in os.environ:
            env[key] = os.environ[key]
    return env


def npm_environment(temp_root: Path, npm_cache: Path) -> dict[str, str]:
    user_config = temp_root / "empty-user.npmrc"
    global_config = temp_root / "empty-global.npmrc"
    user_config.write_text("", encoding="utf-8")
    global_config.write_text("", encoding="utf-8")
    env = base_tool_environment(temp_root)
    env.update({
        "npm_config_cache": str(npm_cache),
        "npm_config_userconfig": str(user_config),
        "npm_config_globalconfig": str(global_config),
        "npm_config_offline": "true",
        "npm_config_ignore_scripts": "true",
        "npm_config_registry": "http://127.0.0.1:9",
        "npm_config_audit": "false",
        "npm_config_fund": "false",
        "npm_config_update_notifier": "false",
    })
    npm_cache.mkdir(parents=True, exist_ok=True)
    return env


def parse_node_version(version_text: str) -> tuple[int, int, int]:
    match = re.search(r"v?(\d+)\.(\d+)\.(\d+)", version_text)
    if not match:
        fail(f"cannot parse Node version: {version_text}")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


def go_environment(go_cache: Path, module_cache: Path, temp_root: Path) -> dict[str, str]:
    env = base_tool_environment(temp_root)
    config_root = temp_root / "go-config"
    config_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    env.update({
        "CGO_ENABLED": "0",
        "GOTOOLCHAIN": "local",
        "GOENV": "off",
        "GOWORK": "off",
        "GOPROXY": "off",
        "GOSUMDB": "off",
        "GOVCS": "*:off",
        "GOCACHE": str(go_cache),
        "GOMODCACHE": str(module_cache),
        "XDG_CONFIG_HOME": str(config_root),
        "APPDATA": str(config_root),
        "LOCALAPPDATA": str(config_root),
    })
    return env


def disable_go_telemetry(go: str, env: dict[str, str]) -> None:
    result = subprocess.run([go, "telemetry", "off"], env=env, check=False, capture_output=True, text=True)
    if result.returncode != 0:
        fail(f"cannot disable Go telemetry in disposable build environment: {result.stderr.strip()}")


def build_env(go_cache: Path, module_cache: Path, target: dict[str, str], temp_root: Path) -> dict[str, str]:
    env = go_environment(go_cache, module_cache, temp_root)
    env.update({
        "GOOS": target["goos"],
        "GOARCH": target["goarch"],
    })
    if target["goarch"] == "amd64":
        env["GOAMD64"] = "v1"
        env.pop("GOARM64", None)
    else:
        env["GOARM64"] = "v8.0"
        env.pop("GOAMD64", None)
    return env


def run_build(go: str, binary: Path, target: dict[str, str], env: dict[str, str]) -> tuple[str, str]:
    binary.parent.mkdir(parents=True, exist_ok=True)
    command = [
        go,
        "build",
        "-mod=readonly",
        "-trimpath",
        "-buildvcs=false",
        "-o",
        str(binary),
        "./cmd/syntra",
    ]
    result = subprocess.run(command, cwd=CORE, env=env, check=False, capture_output=True, text=True)
    if result.returncode != 0:
        fail(
            f"Go build failed for {target['goos']}/{target['goarch']} (exit {result.returncode}):\n"
            f"{result.stdout}{result.stderr}"
        )
    binary.chmod(0o755)
    mode = stat.S_IMODE(binary.stat().st_mode)
    if mode != 0o755 or not binary.is_file():
        fail(f"built executable must be a regular 0755 file: {binary}")

    metadata = subprocess.run([go, "version", "-m", str(binary)], env=env, check=False, capture_output=True, text=True)
    if metadata.returncode != 0:
        fail(f"cannot inspect Go build metadata for {target['package']}: {metadata.stderr.strip()}")
    expected_settings = (
        f"GOOS={target['goos']}",
        f"GOARCH={target['goarch']}",
        "CGO_ENABLED=0",
    )
    build_tokens = set(metadata.stdout.split())
    for setting in expected_settings:
        if setting not in build_tokens:
            fail(f"binary target metadata mismatch for {target['package']}: missing {setting}")
    return metadata.stdout, hashlib.sha256(binary.read_bytes()).hexdigest()


def package_directory_manifest(stage: Path, relative: str, source: Path, mode: int = 0o644) -> None:
    destination = stage / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_bytes(source.read_bytes())
    destination.chmod(mode)


def pack_one(
    npm: str,
    npm_env: dict[str, str],
    package_dir: Path,
    package_name: str,
    version: str,
    stage_out: Path,
    expected_files: dict[str, int],
    private: bool,
) -> dict[str, Any]:
    expected_filename = f"{package_name.removeprefix('@').replace('/', '-')}-{version}.tgz"
    result = subprocess.run(
        [
            npm,
            "pack",
            "--offline",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
            "--json",
            "--registry=http://127.0.0.1:9",
            "--pack-destination",
            str(stage_out),
        ],
        cwd=package_dir,
        env=npm_env,
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        fail(f"npm pack failed for {package_name}:\n{result.stdout}{result.stderr}")
    try:
        packed = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        fail(f"npm pack returned invalid JSON for {package_name}: {error}")
    # npm 12 indexes the report by package name; npm 11 emits an array.
    if isinstance(packed, dict):
        packed = list(packed.values()) if set(packed) == {package_name} else []
    if not isinstance(packed, list) or len(packed) != 1 or packed[0].get("filename") != expected_filename:
        fail(f"npm pack returned an unexpected artifact for {package_name}: {result.stdout.strip()}")

    archive_path = stage_out / expected_filename
    if not archive_path.is_file():
        fail(f"npm pack did not create {expected_filename}")
    files = validate_tarball(archive_path, package_dir, package_name, version, expected_files, private)
    return {
        "name": package_name,
        "version": version,
        "path": str(archive_path),
        "sha256": hashlib.sha256(archive_path.read_bytes()).hexdigest(),
        "files": files,
    }


def validate_tarball(
    archive_path: Path,
    package_dir: Path,
    package_name: str,
    version: str,
    expected_files: dict[str, int],
    private: bool,
) -> list[dict[str, Any]]:
    expected_directories: set[str] = set()
    for relative in expected_files:
        for parent in (PurePosixPath("package") / relative).parents:
            if parent.as_posix() != ".":
                expected_directories.add(parent.as_posix())

    observed_files: dict[str, dict[str, Any]] = {}
    try:
        archive = tarfile.open(archive_path, "r:gz")
    except (OSError, tarfile.TarError) as error:
        fail(f"cannot read npm tarball {archive_path.name}: {error}")
    with archive:
        for member in archive.getmembers():
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or not path.parts or path.parts[0] != "package":
                fail(f"unsafe path in {archive_path.name}: {member.name}")
            if member.isdir():
                if path.as_posix() not in expected_directories:
                    fail(f"unexpected directory in {archive_path.name}: {member.name}")
                continue
            if not member.isfile():
                fail(f"non-regular file in {archive_path.name}: {member.name}")
            relative = PurePosixPath(*path.parts[1:]).as_posix()
            if relative in observed_files:
                fail(f"duplicate file in {archive_path.name}: {relative}")
            expected_mode = expected_files.get(relative)
            if expected_mode is None:
                fail(f"unexpected file in {archive_path.name}: {relative}")
            mode = stat.S_IMODE(member.mode)
            if mode != expected_mode:
                fail(f"mode mismatch in {archive_path.name} for {relative}: {mode:04o} != {expected_mode:04o}")
            stream = archive.extractfile(member)
            if stream is None:
                fail(f"cannot extract file bytes from {archive_path.name}: {relative}")
            contents = stream.read()
            source = package_dir / relative
            if not source.is_file() or contents != source.read_bytes():
                fail(f"file contents differ from staged package for {package_name}: {relative}")
            observed_files[relative] = {
                "path": relative,
                "mode": f"{mode:04o}",
                "sha256": hashlib.sha256(contents).hexdigest(),
            }

    if set(observed_files) != set(expected_files):
        missing = sorted(set(expected_files) - set(observed_files))
        fail(f"incomplete tarball inventory for {package_name}; missing: {', '.join(missing)}")

    manifest_path = "package.json"
    with tarfile.open(archive_path, "r:gz") as archive:
        manifest_member = archive.getmember(f"package/{manifest_path}")
        stream = archive.extractfile(manifest_member)
        if stream is None:
            fail(f"missing manifest in tarball: {package_name}")
        manifest = json.loads(stream.read())
    if manifest.get("name") != package_name or manifest.get("version") != version or manifest.get("private") is not private:
        fail(f"packed manifest metadata mismatch for {package_name}")
    lifecycle_check(manifest, package_name)
    return [observed_files[name] for name in sorted(observed_files)]


def write_artifacts(stage_out: Path, output: Path, artifact_names: set[str]) -> None:
    if output.exists() and (not output.is_dir() or any(output.iterdir())):
        fail(f"refusing to write artifacts into a non-empty output path: {output}")
    staged_names = {entry.name for entry in stage_out.iterdir() if entry.is_file()}
    if staged_names != artifact_names or any(not entry.is_file() for entry in stage_out.iterdir()):
        fail("validated staging directory does not contain exactly the six expected tarballs")
    if output.exists():
        output.rmdir()
    try:
        os.replace(stage_out, output)
    except OSError as error:
        fail(f"cannot atomically place validated tarballs at {output}: {error}")


def stage_release_manifest(
    manifest_path: Path, version: str, metadata: dict[str, Any], description: str,
) -> None:
    manifest, _ = read_manifest(manifest_path)
    manifest.update(metadata, version=version, description=description)
    manifest.pop("scripts", None)
    manifest["files"].append("THIRD-PARTY-NOTICES.md")
    if "optionalDependencies" in manifest:
        manifest["optionalDependencies"] = {
            name: version for name in manifest["optionalDependencies"]
        }
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", help="path to the pinned Go 1.27.3 executable")
    parser.add_argument("--npm", help="path to npm (default: first npm on PATH)")
    parser.add_argument("--output-dir", default="dist/npm", help="artifact directory (default: dist/npm under the repository)")
    parser.add_argument("--go-cache", default=os.environ.get("GOCACHE"), help="explicit Go build cache directory")
    parser.add_argument("--module-cache", default=os.environ.get("GOMODCACHE"), help="pre-populated Go module cache directory")
    parser.add_argument("--release-version", help="stage public manifests for an approved X.Y.Z release")
    parser.add_argument("--repository", help="GitHub owner/repository of the public client")
    args = parser.parse_args()

    private = args.release_version is None
    release_docs = ("LICENSE", "THIRD-PARTY-NOTICES.md")
    if private:
        if args.repository:
            parser.error("--repository requires --release-version")
    else:
        if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.release_version):
            parser.error("--release-version must be a stable X.Y.Z")
        if not args.repository or not re.fullmatch(r"[\w-]+/[\w.-]+", args.repository, re.ASCII):
            parser.error("--repository must be the public GitHub owner/repository")
        for relative in (*release_docs, "README.md"):
            source = ROOT / relative
            if source.is_symlink() or not source.is_file() or not source.stat().st_size:
                fail(f"public packages require a reviewed, nonempty {relative}")
        release_metadata = {
            "private": False,
            "license": "MIT",
            "repository": {"type": "git", "url": f"git+https://github.com/{args.repository}.git"},
            "publishConfig": {"access": "public", "registry": "https://registry.npmjs.org/"},
        }

    go = executable_path(args.go, "go")
    npm = executable_path(args.npm, "npm")
    node = executable_path(None, "node")
    node_version = command_version(node, ["--version"])
    if parse_node_version(node_version) < (22, 12, 0):
        fail(f"Node >=22.12 is required, found {node_version}")
    if not args.go_cache or not args.module_cache:
        fail("set --go-cache and --module-cache to the prepared offline Go caches")
    go_cache = Path(args.go_cache).resolve()
    module_cache = Path(args.module_cache).resolve()
    if not module_cache.is_dir():
        fail(f"prepared Go module cache is missing: {module_cache}")
    go_cache.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="syntra-tool-versions-") as npm_temp_name:
        npm_temp = Path(npm_temp_name)
        version_cache = npm_temp / "cache"
        npm_result = subprocess.run(
            [npm, "--version"],
            check=False,
            capture_output=True,
            text=True,
            env=npm_environment(npm_temp, version_cache),
        )
        if npm_result.returncode != 0:
            fail(f"npm version check failed: {npm_result.stderr.strip()}")
        npm_version = (npm_result.stdout or npm_result.stderr).strip()
        safe_go_env = go_environment(go_cache, module_cache, npm_temp)
        disable_go_telemetry(go, safe_go_env)
        go_version = command_version(go, ["version"], env=safe_go_env)
    if not re.search(r"\bgo1\.27\.3\b", go_version):
        fail(f"Go 1.27.3 is required, found {go_version}")

    root_manifest, source_manifests = validate_manifests()
    version = root_manifest["version"] if private else args.release_version
    output_arg = Path(args.output_dir)
    output = output_arg.resolve() if output_arg.is_absolute() else (ROOT / output_arg).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists() and not output.is_dir():
        fail(f"output path is not a directory: {output}")
    if output.exists() and any(output.iterdir()):
        fail(f"refusing to write artifacts into a non-empty output path: {output}")

    expected_artifact_names = {
        f"{root_manifest['name'].removeprefix('@').replace('/', '-')}-{version}.tgz",
        *(f"{target['package'].removeprefix('@').replace('/', '-')}-{version}.tgz" for target in TARGETS),
    }
    report: dict[str, Any] = {
        "node": node_version,
        "npm": npm_version,
        "go": go_version,
        "version": version,
        "artifacts": [],
    }
    with tempfile.TemporaryDirectory(prefix="syntra-npm-build-", dir=output.parent) as temp_name:
        temp_root = Path(temp_name)
        staged_packages = temp_root / "packages"
        stage_out = temp_root / "artifacts"
        stage_out.mkdir()
        npm_cache = temp_root / "npm-cache"
        npm_env = npm_environment(temp_root, npm_cache)
        disable_go_telemetry(go, go_environment(go_cache, module_cache, temp_root))

        root_stage = staged_packages / "root"
        for relative in ("package.json", *ROOT_FILES):
            mode = 0o755 if relative == "bin/launcher.cjs" else 0o644
            package_directory_manifest(root_stage, relative, ROOT / relative, mode)
        root_expected = {relative: (0o755 if relative == "bin/launcher.cjs" else 0o644) for relative in ("package.json", *ROOT_FILES)}
        if not private:
            stage_release_manifest(root_stage / "package.json", version, release_metadata, "Syntra CLI and local MCP server")
            for relative in release_docs:
                package_directory_manifest(root_stage, relative, ROOT / relative)
                root_expected[relative] = 0o644

        for target in TARGETS:
            package_stage = staged_packages / target["directory"]
            source_manifest = ROOT / "packages" / target["directory"] / "package.json"
            package_directory_manifest(package_stage, "package.json", source_manifest)
            files = {"package.json": 0o644, target["executable"]: 0o755}
            if not private:
                stage_release_manifest(
                    package_stage / "package.json", version, release_metadata,
                    f"Syntra Go executable for {target['os']}/{target['cpu']}",
                )
                for relative in release_docs:
                    package_directory_manifest(package_stage, relative, ROOT / relative)
                    files[relative] = 0o644
            binary = package_stage / target["executable"]
            target_env = build_env(go_cache, module_cache, target, temp_root)
            print(f"Building {target['package']} for {target['goos']}/{target['goarch']} (offline)", file=sys.stderr)
            metadata, binary_sha = run_build(go, binary, target, target_env)
            artifact = pack_one(npm, npm_env, package_stage, target["package"], version, stage_out, files, private)
            artifact["target"] = f"{target['goos']}/{target['goarch']}"
            artifact["binarySha256"] = binary_sha
            artifact["goBuildMetadata"] = [
                " ".join(line.split()) for line in metadata.splitlines()
                if line.split() and line.split()[0] == "build"
            ]
            report["artifacts"].append(artifact)

        root_artifact = pack_one(
            npm,
            npm_env,
            root_stage,
            root_manifest["name"],
            version,
            stage_out,
            root_expected,
            private,
        )
        report["artifacts"].append(root_artifact)

        for path, original in source_manifests.items():
            if path.read_bytes() != original:
                fail(f"source manifest changed during package build: {path.relative_to(ROOT)}")

        report["artifacts"].sort(key=lambda artifact: artifact["name"])
        write_artifacts(stage_out, output, expected_artifact_names)
        for artifact in report["artifacts"]:
            artifact["path"] = str(output / Path(artifact["path"]).name)

    print(json.dumps(report, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except BuildError as error:
        print(f"build-npm-packages: {error}", file=sys.stderr)
        raise SystemExit(1)
