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

test('launcher exposes package version without native binary', () => {
  for (const arg of ['--version', 'version']) {
    const result = spawnSync(process.execPath, [launcherPath, arg], { encoding: 'utf8' });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, `${packageVersion}\n`);
  }
});

const { update } = require('../bin/update.cjs');
function updateFixture(t, options = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'syntra-update-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const root = path.join(dir, ...(process.platform === 'win32' ? [] : ['lib']), 'node_modules');
  const pkg = path.join(root, '@synapsync', 'syntra-mcp');
  fs.mkdirSync(pkg, { recursive: true });
  let version = options.installed || '1.0.0';
  const calls = [], output = [], errors = [];
  const deps = {
    packageDir: pkg, env: options.env || { HOME: dir, USERPROFILE: dir }, tty: options.tty || false,
    out: line => output.push(line), err: line => errors.push(line),
    readVersion: () => version,
    prompt: async () => options.answer || 'no',
    npm: args => {
      calls.push(args);
      if (args[0] === 'view') return { status: 0, stdout: options.latest ?? '1.0.1' };
      if (args[0] === 'root') return { status: 0, stdout: root };
      if (!options.failure && !options.mismatch) version = options.latest ?? '1.0.1';
      return { status: options.failure ? 1 : 0 };
    },
  };
  return { deps, calls, output, errors, dir, root, pkg };
}

test('update check reports versions and distinct status', async t => {
  for (const [latest, code] of [['1.0.0', 0], ['1.0.1', 10], ['0.9.0', 0]]) {
    const f = updateFixture(t, { latest });
    assert.equal(await update(['--check'], f.deps), code);
    assert.match(f.output.join('\n'), /Installed version: 1\.0\.0\nLatest published version:/);
    assert.equal(f.calls.length, 1);
  }
});

test('update confirms or refuses non-interactive execution unless --yes', async t => {
  const declined = updateFixture(t, { tty: true });
  assert.equal(await update([], declined.deps), 1);
  assert.equal(declined.calls.length, 2);
  const accepted = updateFixture(t, { tty: true, answer: 'yes' });
  assert.equal(await update([], accepted.deps), 0);
  assert.deepEqual(accepted.calls.at(-1), ['install', '-g', '--prefix', accepted.dir, '@synapsync/syntra-mcp@1.0.1']);
  const noTTY = updateFixture(t);
  assert.equal(await update([], noTTY.deps), 1);
  assert.match(noTTY.errors.join(' '), /TTY/);
  const yes = updateFixture(t);
  assert.equal(await update(['--yes'], yes.deps), 0);
  assert.match(yes.output.join(' '), /Updated to 1\.0\.1/);
});

test('update does not install a previously confirmed version over a concurrently changed installation', async t => {
  const f = updateFixture(t, { tty: true });
  let observed = '1.0.0';
  f.deps.readVersion = () => observed;
  f.deps.prompt = async () => {
    observed = '1.0.2';
    return 'yes';
  };
  assert.equal(await update([], f.deps), 1);
  assert.match(f.errors.join(' '), /installation changed during confirmation/);
  assert.ok(f.calls.every(args => args[0] !== 'install'));
});

test('update handles npm failure, post-install mismatch, offline and unsafe versions', async t => {
  for (const options of [{ failure: true }, { mismatch: true }]) {
    const f = updateFixture(t, options);
    assert.equal(await update(['--yes'], f.deps), 1);
    assert.match(f.errors.join(' '), /Manual command:/);
    assert.doesNotMatch(f.output.join(' '), /Updated to/);
  }
  const offline = updateFixture(t, { env: { SYNTRA_UPDATE_OFFLINE: '1' } });
  assert.equal(await update(['--check'], offline.deps), 1);
  assert.equal(offline.calls.length, 0);
  for (const latest of ['1.0.1;bad', '01.0.0', '1.0.1-beta', '1.0.1\nmalicious']) {
    const f = updateFixture(t, { latest });
    assert.equal(await update(['--yes'], f.deps), 1);
    assert.equal(f.calls.length, 1);
  }
  const invalidInstalled = updateFixture(t, { installed: '1.0.0-beta' });
  assert.equal(await update(['--check'], invalidInstalled.deps), 1);
  assert.equal(invalidInstalled.calls.length, 0);
});

test('update detects npx and local installations and never installs', async t => {
  for (const [location, expected] of [['_npx/abc/node_modules/@synapsync/syntra-mcp', 'npx -y'], ['checkout', 'npm install -g']]) {
    const f = updateFixture(t);
    f.deps.packageDir = path.join(f.dir, location);
    fs.mkdirSync(f.deps.packageDir, { recursive: true });
    assert.equal(await update(['--yes'], f.deps), 1);
    assert.match(f.errors.join(' '), new RegExp(expected));
    assert.equal(f.calls.length, 1);
  }
});

test('launcher invokes installation npm with fixed arguments and reports check exit status', t => {
  const f = updateFixture(t);
  fs.cpSync(path.join(root, 'bin'), path.join(f.pkg, 'bin'), { recursive: true });
  fs.writeFileSync(path.join(f.pkg, 'package.json'), JSON.stringify({ name: '@synapsync/syntra-mcp', version: '1.0.0' }));
  const associatedNpm = path.join(f.root, 'npm', 'bin', 'npm-cli.js');
  fs.mkdirSync(path.dirname(associatedNpm), { recursive: true });
  fs.writeFileSync(associatedNpm, `
    require('node:fs').writeFileSync(process.env.FAKE_NPM_ARGS, JSON.stringify(process.argv.slice(2)));
    console.log('1.2.3');
  `);
  const argsFile = path.join(f.dir, 'args');
  const result = spawnSync(process.execPath, [path.join(f.pkg, 'bin', 'launcher.cjs'), 'update', '--check'], {
    encoding: 'utf8', env: { HOME: f.dir, USERPROFILE: f.dir, PATH: '', FAKE_NPM_ARGS: argsFile },
  });
  assert.equal(result.status, 10, result.stderr);
  assert.deepEqual(JSON.parse(fs.readFileSync(argsFile, 'utf8')), ['view', '@synapsync/syntra-mcp@latest', 'version', '--prefer-online']);
  assert.match(result.stdout, /codex: absent in inspected config/);
});

test('launcher delegates both help forms to the native executable', { skip: process.platform === 'win32' }, t => {
  const fixture = fixturePackage(t);
  for (const args of [['--help'], ['help', 'prepare']]) {
    const result = spawnSync(process.execPath, [launcherPath, ...args], {
      cwd: fixture.directory, env: launcherEnvironment(fixture.modules), input: '', encoding: 'utf8', timeout: 10000,
    });
    assert.equal(result.status, 0, result.stderr);
    const firstLine = result.stdout.split('\n')[0];
    assert.deepEqual(JSON.parse(firstLine), { args, input: '' });
    if (args.length === 1) assert.match(result.stdout, /update[\s\S]*--version/);
  }
});

test('global update ignores an unrelated PATH npm and reports every configured mode without changing config', t => {
  if (process.platform === 'win32') return t.skip('POSIX fake npm fixture');
  const f = updateFixture(t);
  const tools = path.join(f.dir, 'tools');
  const home = path.join(f.dir, 'home');
  const xdg = path.join(f.dir, 'xdg');
  fs.mkdirSync(tools);
  fs.mkdirSync(path.join(home, '.codex'), { recursive: true });
  fs.mkdirSync(path.join(xdg, 'opencode'), { recursive: true });
  fs.cpSync(path.join(root, 'bin'), path.join(f.pkg, 'bin'), { recursive: true });
  const manifest = path.join(f.pkg, 'package.json');
  fs.writeFileSync(manifest, '{"name":"@synapsync/syntra-mcp","private":false,"version":"1.0.0"}\n');
  const configs = new Map([
    [path.join(home, '.codex', 'config.toml'),
      '[mcp_servers.syntra]\ncommand = "npx"\nargs = ["-y", "@synapsync/syntra-mcp@1.0.0", "mcp"]\n'],
    [path.join(home, '.claude.json'),
      '{"mcpServers":{"syntra":{"command":"unrelated","args":["@synapsync/syntra-mcp@1.0.0","mcp"]}}}'],
    [path.join(xdg, 'opencode', 'opencode.jsonc'),
      '{"mcp":{"servers":{"syntra":{"type":"local","command":["npx","-y","@synapsync/syntra-mcp@1.0.1","mcp"]}}}}'],
  ]);
  for (const [file, source] of configs) fs.writeFileSync(file, source);

  fs.writeFileSync(path.join(tools, 'npm'), '#!/bin/sh\nexit 99\n', { mode: 0o755 });
  const npm = path.join(f.root, 'npm', 'bin', 'npm-cli.js');
  fs.mkdirSync(path.dirname(npm), { recursive: true });
  fs.writeFileSync(npm, `
    const fs = require('node:fs');
    const [command, ...args] = process.argv.slice(2);
    const prefix = process.env.FAKE_PREFIX;
    if (command === 'view') {
      if (JSON.stringify(args) !== JSON.stringify(['@synapsync/syntra-mcp@latest', 'version', '--prefer-online'])) process.exit(2);
      console.log('1.0.1');
    } else if (command === 'root') {
      if (JSON.stringify(args) !== JSON.stringify(['-g', '--prefix', prefix])) process.exit(2);
      console.log(process.env.FAKE_GLOBAL_ROOT);
    } else if (command === 'install') {
      if (JSON.stringify(args) !== JSON.stringify(['-g', '--prefix', prefix, '@synapsync/syntra-mcp@1.0.1'])) process.exit(2);
      fs.writeFileSync(process.env.FAKE_MANIFEST, '{"name":"@synapsync/syntra-mcp","private":false,"version":"1.0.1"}');
    } else process.exit(2);
  `);
  const env = { HOME: home, USERPROFILE: home, XDG_CONFIG_HOME: xdg, PATH: tools,
    FAKE_GLOBAL_ROOT: f.root, FAKE_PREFIX: f.dir, FAKE_MANIFEST: manifest };
  const invoke = (...args) => spawnSync(process.execPath, [path.join(f.pkg, 'bin', 'launcher.cjs'), 'update', ...args], {
    env, encoding: 'utf8', timeout: 10000,
  });
  const checked = invoke('--check');
  assert.equal(checked.status, 10, checked.stderr);
  assert.match(checked.stdout, /codex: pinned to 1\.0\.0; active process not verified/);
  assert.match(checked.stdout, /claude-code: unresolved config or custom launcher/);
  assert.match(checked.stdout, /opencode: pinned to 1\.0\.1; active process not verified/);
  const updated = invoke('--yes');
  assert.equal(updated.status, 0, updated.stderr);
  assert.match(updated.stdout, /Updated to 1\.0\.1\./);
  const current = invoke('--check');
  assert.equal(current.status, 0, current.stderr);
  assert.match(current.stdout, /codex: pinned to 1\.0\.0; active process not verified/);
  for (const [file, source] of configs) assert.equal(fs.readFileSync(file, 'utf8'), source);
});
