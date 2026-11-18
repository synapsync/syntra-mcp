#!/usr/bin/env python3
"""Capture six npm tarballs for manual review and later digest binding.

The digest binds verification to the reviewed manifest; it is not a signature
or proof that the captured files are approved for release.
"""
import argparse, hashlib, json, pathlib, subprocess, sys

TARGETS = ['darwin-arm64', 'darwin-x64', 'linux-arm64', 'linux-x64', 'win32-x64']

def sha(data): return hashlib.sha256(data).hexdigest()
def contents(archive, member):
    return subprocess.run(['tar', '-xOzf', str(archive), member], check=True, stdout=subprocess.PIPE).stdout

def capture(directory, private):
    files = sorted(directory.glob('*.tgz'))
    if len(files) != 6 or len(list(directory.iterdir())) != 6: raise ValueError('expected exactly six tarballs')
    manifests = {}
    for f in files:
        if f.is_symlink() or not f.is_file(): raise ValueError('tarball must be a regular file')
        names = subprocess.run(['tar', '-tzf', str(f)], check=True, stdout=subprocess.PIPE, text=True).stdout.splitlines()
        if len(names) != len(set(names)) or any(not n.startswith('package/') or '..' in n.split('/') for n in names): raise ValueError('unsafe or duplicate tar member')
        pkg = json.loads(contents(f, 'package/package.json'))
        if pkg.get('private') is not private: raise ValueError('unexpected package visibility: ' + f.name)
        if pkg['name'] in manifests: raise ValueError('duplicate package name')
        manifests[pkg['name']] = (f, pkg, names)
    rootname = '@synapsync/syntra-mcp'
    root, pkg, _ = manifests[rootname]
    version = pkg['version']
    native_prefix = '@synapsync/syntra-'
    expected_names = {rootname, *(native_prefix + t for t in TARGETS)}
    if set(manifests) != expected_names: raise ValueError('unexpected package inventory')
    expected_deps = {native_prefix + t: version for t in TARGETS}
    if pkg.get('optionalDependencies') != expected_deps: raise ValueError('native dependency versions differ')
    artifacts, targets, inventory = {}, {}, {}
    for name, (f, p, names) in sorted(manifests.items()):
        if p['version'] != version: raise ValueError('version mismatch: ' + name)
        artifacts[f.name] = sha(f.read_bytes())
        inventory[f.name] = names
        if name != rootname:
            target = name[len(native_prefix):]
            binary = 'bin/syntra.exe' if target.startswith('win32-') else 'bin/syntra'
            target_key = target.replace('-', '/')
            platform, arch = target_key.split('/')
            if p.get('os') != [platform] or p.get('cpu') != [arch]: raise ValueError('platform metadata mismatch: ' + name)
            targets[target_key] = dict(artifact=f.name, packageName=name, binary=binary,
                                       binarySHA256=sha(contents(f, 'package/' + binary)))
    return dict(version=version, private=private, rootTarball=root.name, artifacts=artifacts, targets=targets, inventory=inventory)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=pathlib.Path)
    parser.add_argument("manifest", type=pathlib.Path)
    parser.add_argument("--release", action="store_true", help="expect staged public packages")
    args = parser.parse_args()
    directory, out = args.directory, args.manifest
    if not directory.is_absolute() or not out.is_absolute():
        raise ValueError("paths must be absolute")
    data = (json.dumps(capture(directory, not args.release), indent=2, sort_keys=True) + "\n").encode()
    with out.open("xb") as f:
        f.write(data)
    print("Review manifest and all tar member inventories:", out)
    print("manifest SHA-256:", sha(data))

if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        print("FAIL:", error, file=sys.stderr)
        sys.exit(1)
