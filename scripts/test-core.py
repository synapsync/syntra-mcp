#!/usr/bin/env python3
"""Run the CLI and MCP source tests on the current Go target."""

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("--go", default="go")
parser.add_argument("--race", action="store_true")
args = parser.parse_args()
core = pathlib.Path(__file__).resolve().parents[1] / "core"

settings = json.loads(subprocess.check_output(
    [args.go, "env", "-json", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE",
     "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH"],
    cwd=core,
    text=True,
))
if (settings.pop("GOHOSTOS"), settings.pop("GOHOSTARCH")) != (settings["GOOS"], settings["GOARCH"]):
    parser.error("the Go target must match the Go toolchain host to run tests")
env = dict(os.environ, **settings)
go_bin = pathlib.Path(settings["GOROOT"]) / "bin"
suffix = ".exe" if os.name == "nt" else ""
go = str(go_bin / ("go" + suffix))
env["PATH"] = str(go_bin) + os.pathsep + env.get("PATH", "")
env["GOTOOLCHAIN"] = "local"


def run(*arguments):
    subprocess.run([go, *arguments], cwd=core, env=env, check=True)


run("version")
race = ["-race"] if args.race else []
run("test", "-mod=readonly", "-count=1", "-timeout=4m", *race, "./...")
run("vet", "-mod=readonly", "./...")
with tempfile.TemporaryDirectory(prefix="syntra-mcp-") as build:
    binary = pathlib.Path(build) / ("syntra" + suffix)
    run("build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", str(binary), "./cmd/syntra")
    run("version", "-m", str(binary))
    digest = hashlib.sha256()
    with binary.open("rb") as stream:
        for chunk in iter(lambda: stream.read(65536), b""):
            digest.update(chunk)
    print("CLI SHA-256", digest.hexdigest())
print("Standalone CLI and MCP tests, vet, and build passed.")
