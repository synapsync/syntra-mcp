#!/usr/bin/env python3
"""Reserve a stable npm patch version for the current main commit."""

import json
import os
import re
import subprocess
import urllib.request

VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")


def command(*arguments):
    return subprocess.check_output(arguments, text=True).strip()


def stable_versions(tags):
    return {
        tag[1:]: tuple(map(int, tag[1:].split(".")))
        for tag in tags
        if tag.startswith("v") and VERSION.fullmatch(tag[1:])
    }


def choose_version(latest, tags, commit_tags):
    if not VERSION.fullmatch(latest):
        raise ValueError("the npm latest tag must point to a stable version")
    reserved = stable_versions(commit_tags)
    if len(reserved) > 1:
        raise ValueError("the commit already has multiple release tags")
    if reserved:
        return next(iter(reserved)), False

    published = tuple(map(int, latest.split(".")))
    versions = [published, *stable_versions(tags).values()]
    major, minor, patch = max(versions)
    return f"{major}.{minor}.{patch + 1}", True


def main():
    repository = os.environ["GITHUB_REPOSITORY"]
    commit = os.environ["GITHUB_SHA"]
    refs = f"repos/{repository}/git/ref/heads/main"
    if command("git", "rev-parse", "HEAD") != commit:
        raise ValueError("checked out a different commit from the workflow run")
    if command("gh", "api", refs, "--jq", ".object.sha") != commit:
        print("A newer main commit is available. Skipping this release.")
        return

    with urllib.request.urlopen(
        "https://registry.npmjs.org/@synapsync%2fsyntra-mcp/latest", timeout=15
    ) as response:
        latest = json.load(response)["version"]
    version, create = choose_version(
        latest, command("git", "tag", "--list").splitlines(),
        command("git", "tag", "--points-at", commit).splitlines(),
    )
    if create:
        command(
            "gh", "api", "--method", "POST", f"repos/{repository}/git/refs",
            "-f", f"ref=refs/tags/v{version}", "-f", f"sha={commit}",
        )
    print(f"Publishing version {version} from {commit}.")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"version={version}\n")


if __name__ == "__main__":
    main()
