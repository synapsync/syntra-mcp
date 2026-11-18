'use strict';

const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const root = path.resolve(__dirname, '..');
const launcherPath = path.join(root, 'bin', 'launcher.cjs');
const launcher = require(launcherPath);
const packageVersion = '0.0.0-handoff';
const targets = [
  { directory: 'linux-x64', platform: 'linux', architecture: 'x64', packageName: '@synapsync/syntra-linux-x64', executable: 'bin/syntra' },
  { directory: 'linux-arm64', platform: 'linux', architecture: 'arm64', packageName: '@synapsync/syntra-linux-arm64', executable: 'bin/syntra' },
  { directory: 'darwin-x64', platform: 'darwin', architecture: 'x64', packageName: '@synapsync/syntra-darwin-x64', executable: 'bin/syntra' },
  { directory: 'darwin-arm64', platform: 'darwin', architecture: 'arm64', packageName: '@synapsync/syntra-darwin-arm64', executable: 'bin/syntra' },
  { directory: 'win32-x64', platform: 'win32', architecture: 'x64', packageName: '@synapsync/syntra-win32-x64', executable: 'bin/syntra.exe' },
];
const runtimeTarget = targets.find(target => target.platform === process.platform && target.architecture === process.arch);

function fixturePackage(t, installedVersion = packageVersion) {
  assert.ok(runtimeTarget, `no synthetic executable fixture for ${process.platform}/${process.arch}`);
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'syntra launcher fixture '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const modules = path.join(directory, 'node_modules');
  const packageDirectory = path.join(modules, ...runtimeTarget.packageName.split('/'));
  const executable = path.join(packageDirectory, ...runtimeTarget.executable.split('/'));
  fs.mkdirSync(path.dirname(executable), { recursive: true });
  fs.writeFileSync(path.join(packageDirectory, 'package.json'), JSON.stringify({
    name: runtimeTarget.packageName,
    version: installedVersion,
    private: true,
  }));
  fs.writeFileSync(executable, [
    '#!/usr/bin/env node',
    "'use strict';",
    'const args = process.argv.slice(2);',
    "if (args.includes('--fixture-hang')) { process.stderr.write('fixture-ready\\n'); setInterval(() => {}, 1000); }",
    "else { let input = ''; process.stdin.setEncoding('utf8'); process.stdin.on('data', chunk => { input += chunk; }); process.stdin.on('end', () => { process.stdout.write(JSON.stringify({ args, input }) + '\\n'); process.stderr.write('fixture stderr\\n'); const exit = args.find(arg => arg.startsWith('--fixture-exit=')); process.exitCode = exit ? Number(exit.slice('--fixture-exit='.length)) : 0; }); }",
    '',
  ].join('\n'), { mode: 0o755 });
  return { directory, modules, executable };
}

function launcherEnvironment(modules) {
  return { ...process.env, NODE_PATH: modules };
}

test('root and five platform manifests are private, version-matched and hook-free', () => {
  const rootPackage = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
  assert.equal(rootPackage.name, '@synapsync/syntra-mcp');
  assert.equal(rootPackage.version, packageVersion);
  assert.equal(rootPackage.private, true);
  assert.equal(rootPackage.engines.node, '>=22.12');
  assert.deepEqual(rootPackage.bin, { syntra: 'bin/launcher.cjs' });
  assert.ok(rootPackage.files.includes('skills/syntra/**'), 'root package must bundle the existing skill');
  assert.ok(fs.existsSync(path.join(root, 'skills', 'syntra', 'SKILL.md')));
  assert.deepEqual(
    rootPackage.optionalDependencies,
    Object.fromEntries(targets.map(target => [target.packageName, packageVersion])),
  );

  const lifecycleHooks = new Set(['preinstall', 'install', 'postinstall', 'prepublish', 'prepare']);
  for (const manifest of [rootPackage]) {
    for (const hook of lifecycleHooks) assert.equal(manifest.scripts?.[hook], undefined, `${manifest.name} must not define ${hook}`);
  }
  for (const target of targets) {
    const manifestPath = path.join(root, 'packages', target.directory, 'package.json');
    const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
    assert.equal(manifest.name, target.packageName);
    assert.equal(manifest.version, packageVersion);
    assert.equal(manifest.private, true);
    assert.deepEqual(manifest.os, [target.platform]);
    assert.deepEqual(manifest.cpu, [target.architecture]);
    assert.ok(manifest.files.includes(target.executable));
    for (const hook of lifecycleHooks) assert.equal(manifest.scripts?.[hook], undefined, `${manifest.name} must not define ${hook}`);
  }
});

test('launcher maps only the five supported platform tuples', () => {
  for (const target of targets) {
    assert.deepEqual(launcher.targetFor(target.platform, target.architecture), {
      packageName: target.packageName,
      executable: target.executable,
    });
  }
  assert.match(launcher.signalNames('win32').join(','), /SIGBREAK/);
  assert.equal(launcher.signalExitCode('SIGTERM'), 143);
  assert.throws(
    () => launcher.targetFor('freebsd', 'arm64'),
    /Syntra does not provide a binary for freebsd\/arm64.*Supported targets:/,
  );
});

test('missing optional package produces actionable local guidance', () => {
  const missing = Object.assign(new Error('private resolution detail'), { code: 'MODULE_NOT_FOUND' });
  assert.throws(
    () => launcher.resolveBinary('linux', 'x64', () => { throw missing; }),
    /The Syntra binary for linux\/x64 is missing.*npm install --include=optional/,
  );
});

test('launcher requires the binary beside the exact validated manifest', t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'syntra nested package fixture '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const exactPackage = path.join(directory, 'nested', 'node_modules', '@synapsync', 'syntra-darwin-arm64');
  const exactManifest = path.join(exactPackage, 'package.json');
  const ancestorBinary = path.join(directory, 'ancestor', 'node_modules', '@synapsync', 'syntra-darwin-arm64', 'bin', 'syntra');
  fs.mkdirSync(exactPackage, { recursive: true });
  fs.mkdirSync(path.dirname(ancestorBinary), { recursive: true });
  fs.writeFileSync(exactManifest, JSON.stringify({
    name: '@synapsync/syntra-darwin-arm64',
    version: packageVersion,
  }));
  fs.writeFileSync(ancestorBinary, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  let attemptedAncestorResolution = false;
  const resolve = request => {
    if (request === '@synapsync/syntra-darwin-arm64/package.json') return exactManifest;
    if (request === '@synapsync/syntra-darwin-arm64/bin/syntra') {
      attemptedAncestorResolution = true;
      return ancestorBinary;
    }
    throw Object.assign(new Error('not found'), { code: 'MODULE_NOT_FOUND' });
  };

  assert.throws(
    () => launcher.resolveBinary('darwin', 'arm64', resolve),
    /The Syntra binary for darwin\/arm64 is missing/,
  );
  assert.equal(attemptedAncestorResolution, false, 'launcher must use the binary under the exact manifest directory');
});

test('malformed platform manifest reports a sanitized version mismatch', t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'syntra malformed manifest fixture '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const manifestPath = path.join(directory, 'package.json');
  fs.writeFileSync(manifestPath, 'null');
  assert.throws(
    () => launcher.resolveBinary('linux', 'x64', request => {
      if (request === '@synapsync/syntra-linux-x64/package.json') return manifestPath;
      throw Object.assign(new Error('unexpected lookup'), { code: 'MODULE_NOT_FOUND' });
    }),
    /does not match launcher version 0\.0\.0-handoff/,
  );
});

test('launcher rejects a different-version optional package before starting it', t => {
  const fixture = fixturePackage(t, '0.0.0-mismatch');
  const result = spawnSync(process.execPath, [launcherPath, 'should-not-run'], {
    cwd: fixture.directory,
    env: launcherEnvironment(fixture.modules),
    encoding: 'utf8',
    timeout: 10000,
  });

  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /does not match launcher version 0\.0\.0-handoff.*Reinstall/);
  assert.equal(result.stdout, '');
});

test('launcher preserves argv, stdio, a spaced executable path and the child exit code', { skip: process.platform === 'win32' }, t => {
  const fixture = fixturePackage(t);
  assert.ok(fixture.executable.includes(' '), 'test executable path must contain spaces');
  const marker = path.join(fixture.directory, 'must not be created');
  const args = ['two words', 'semi;colon', `$(touch ${marker})`, '--fixture-exit=23'];
  const result = spawnSync(process.execPath, [launcherPath, ...args], {
    cwd: fixture.directory,
    env: launcherEnvironment(fixture.modules),
    input: 'stdin payload\n',
    encoding: 'utf8',
    timeout: 10000,
  });

  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.signal, null);
  assert.equal(result.status, 23);
  assert.deepEqual(JSON.parse(result.stdout), { args, input: 'stdin payload\n' });
  assert.equal(result.stderr, 'fixture stderr\n');
  assert.equal(fs.existsSync(marker), false, 'arguments must not be evaluated by a shell');
});

test('launcher forwards SIGTERM and exits by the same signal on POSIX', { skip: process.platform === 'win32' }, async t => {
  const fixture = fixturePackage(t);
  const child = spawn(process.execPath, [launcherPath, '--fixture-hang'], {
    cwd: fixture.directory,
    env: launcherEnvironment(fixture.modules),
    stdio: ['ignore', 'ignore', 'pipe'],
    detached: true,
  });
  let stderr = '';
  let signalSent = false;
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', chunk => {
    stderr += chunk;
    if (!signalSent && stderr.includes('fixture-ready')) {
      signalSent = true;
      child.kill('SIGTERM');
    }
  });

  const result = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      try { process.kill(-child.pid, 'SIGKILL'); } catch {}
      reject(new Error('launcher and fixture did not exit after SIGTERM'));
    }, 5000);
    child.once('error', error => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once('exit', (code, signal) => {
      clearTimeout(timeout);
      resolve({ code, signal });
    });
  });
  assert.equal(signalSent, true, `fixture never started; stderr was ${stderr}`);
  assert.equal(result.code, null);
  assert.equal(result.signal, 'SIGTERM');
});
