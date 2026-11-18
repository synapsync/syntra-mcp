#!/usr/bin/env node
'use strict';

// Validate the reviewed candidate rather than the frozen Sprint 3 artifacts.
const assert = require('node:assert/strict');
const { sha256, findExecutable, isolatedEnvironment, run, runMCP, checkMCP } = require('./test-npm-artifacts.cjs');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

function fail(message) { throw new Error(message); }
function loadCandidate(args) {
  const fromRegistry = args.at(-1) === '--from-registry';
  if (fromRegistry) args = args.slice(0, -1);
  if (args.length !== 6 || args[0] !== '--artifacts' || args[2] !== '--manifest' || args[4] !== '--manifest-sha256') fail('usage: node test-setup-artifacts.cjs --artifacts /absolute/dir --manifest /absolute/file --manifest-sha256 <reviewed sha256> [--from-registry]');
  const [directory, manifestFile, digest] = [args[1], args[3], args[5]];
  if (!path.isAbsolute(directory) || !path.isAbsolute(manifestFile) || !/^[a-f0-9]{64}$/.test(digest)) fail('absolute paths and a lowercase SHA-256 are required');
  assert.equal(sha256(manifestFile), digest, 'reviewed manifest digest mismatch');
  const candidate = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));
  assert.ok(fs.lstatSync(directory).isDirectory() && !fs.lstatSync(directory).isSymbolicLink(), 'artifact directory must be real');
  const files = Object.keys(candidate.artifacts || {}).sort();
  assert.equal(files.length, 6, 'six artifacts required');
  assert.deepEqual(fs.readdirSync(directory).sort(), files, 'unexpected artifact inventory');
  for (const name of files) {
    assert.ok(name === path.basename(name) && /^[a-f0-9]{64}$/.test(candidate.artifacts[name]), 'invalid artifact record');
    const file = path.join(directory, name), stat = fs.lstatSync(file);
    assert.ok(stat.isFile() && !stat.isSymbolicLink(), 'artifact must be a regular file');
    assert.equal(sha256(file), candidate.artifacts[name], `${name} hash differs`);
  }
  assert.ok(files.includes(candidate.rootTarball), 'root artifact missing');
  const targets = ['darwin/arm64', 'darwin/x64', 'linux/arm64', 'linux/x64', 'win32/x64'];
  assert.deepEqual(Object.keys(candidate.targets || {}).sort(), targets.sort(), 'target inventory differs');
  for (const name of targets) {
    const t = candidate.targets[name];
    assert.equal(t.packageName, `@synapsync/syntra-${name.replace('/', '-')}`);
    assert.ok(files.includes(t.artifact) && /^[a-f0-9]{64}$/.test(t.binarySHA256), 'invalid target record');
    assert.equal(t.binary, name.startsWith('win32/') ? 'bin/syntra.exe' : 'bin/syntra');
  }
  assert.deepEqual([candidate.rootTarball, ...targets.map(name => candidate.targets[name].artifact)].sort(), files, 'each target must reference a distinct artifact');
  assert.ok(typeof candidate.version === 'string' && candidate.version.length > 0, 'version missing');
  if (fromRegistry) assert.equal(candidate.private, false, 'registry smoke requires a public candidate');
  return { directory, candidate, digest, fromRegistry };
}
function checkSetup(command, root, baseEnv, scratch, version) {
  const bundledSkill = path.join(root, 'node_modules', '@synapsync', 'syntra-mcp', 'skills', 'syntra');
  assert.ok(fs.statSync(path.join(bundledSkill, 'SKILL.md')).isFile(), 'bundled skill missing');
  function tree(directory, modes = false) {
    if (!fs.existsSync(directory)) return {};
    const out = {};
    function visit(dir, prefix) {
      for (const name of fs.readdirSync(dir).sort()) {
        const file = path.join(dir, name), relative = path.join(prefix, name);
        const stat = fs.lstatSync(file);
        assert.ok(!stat.isSymbolicLink(), `unexpected symlink ${relative}`);
        if (stat.isDirectory()) { out[relative + '/'] = 'directory'; visit(file, relative); }
        else if (stat.isFile()) out[relative] = modes ? { bytes: fs.readFileSync(file).toString('base64'), mode: stat.mode & 0o777 } : fs.readFileSync(file).toString('base64');
        else fail(`unexpected file type ${relative}`);
      }
    }
    visit(directory, ''); return out;
  }
  function fixture(host, suffix, claudeDefault = false) {
    const home = path.join(scratch, `setup-${host}-${suffix}`, 'home');
    fs.mkdirSync(home, { recursive: true, mode: 0o700 });
    const env = { ...baseEnv, HOME: home, USERPROFILE: home,
      XDG_CONFIG_HOME: path.join(home, 'xdg-config'), CODEX_HOME: path.join(home, 'codex-config'),
      CLAUDE_CONFIG_DIR: path.join(home, 'claude-config') };
    if (claudeDefault) delete env.CLAUDE_CONFIG_DIR;
    const configs = {
      codex: { file: path.join(home, 'codex-config', 'config.toml'), seed: '# preserved comment\nmodel = "sentinel-model"\n', skill: path.join(home, '.agents', 'skills', 'syntra') },
      'claude-code': { file: claudeDefault ? path.join(home, '.claude.json') : path.join(home, 'claude-config', '.claude.json'), seed: '{\n  "sentinel": {"value": 42}\n}\n', skill: path.join(home, claudeDefault ? '.claude' : 'claude-config', 'skills', 'syntra') },
      opencode: { file: path.join(home, 'xdg-config', 'opencode', 'opencode.jsonc'), seed: '{\n  // preserved comment\n  "sentinel": {"value": 42}\n}\n', skill: path.join(home, 'xdg-config', 'opencode', 'skills', 'syntra') },
    };
    const config = configs[host]; fs.mkdirSync(path.dirname(config.file), {recursive:true, mode:0o700});
    fs.writeFileSync(config.file, config.seed, {mode:0o600});
    return {home, env, ...config};
  }
  function invoke(f, host, dry = false) {
    return run(command, ['setup', '--agent', host, ...(dry ? ['--dry-run'] : [])], {cwd: root, env:f.env});
  }
  function rejected(result, label) {
    assert.equal(result.signal, null, `${label} terminated by a signal`);
    assert.ok(Number.isInteger(result.status) && result.status !== 0, `${label} unexpectedly succeeded`);
  }
  for (const host of ['codex', 'claude-code', 'opencode']) {
    const absent = fixture(host, 'absent');
    fs.rmSync(absent.file);
    const absentBefore = tree(absent.home, true);
    const absentPreview = invoke(absent, host, true);
    assert.equal(absentPreview.status, 0, `${host} absent-file preview failed: ${absentPreview.stderr}`);
    assert.deepEqual(tree(absent.home, true), absentBefore, `${host} absent-file preview wrote files`);
    if (host === 'opencode') {
      const defaultFile = path.join(path.dirname(absent.file), 'opencode.json');
      assert.ok(absentPreview.stdout.includes(defaultFile), 'OpenCode preview must select the default JSON path');
      assert.equal(invoke(absent, host).status, 0, 'OpenCode default-file setup failed');
      assert.ok(fs.existsSync(defaultFile) && !fs.existsSync(absent.file), 'OpenCode setup must create the default JSON path');
    }
    if (host === 'opencode') {
      const ambiguous = fixture(host, 'ambiguous');
      fs.writeFileSync(path.join(path.dirname(ambiguous.file), 'opencode.json'), '{}\n');
      const before = tree(ambiguous.home, true);
      rejected(invoke(ambiguous, host, true), 'OpenCode ambiguous preview');
      rejected(invoke(ambiguous, host), 'OpenCode ambiguous setup');
      assert.deepEqual(tree(ambiguous.home, true), before, 'OpenCode ambiguous files changed');
    }
    if (host === 'claude-code') {
      const defaults = fixture(host, 'default-paths', true);
      const before = tree(defaults.home, true);
      const preview = invoke(defaults, host, true);
      assert.equal(preview.status, 0, 'Claude default-path preview failed');
      assert.deepEqual(tree(defaults.home, true), before, 'Claude default-path preview wrote files');
      assert.equal(invoke(defaults, host).status, 0, 'Claude default-path setup failed');
      assert.ok(fs.existsSync(path.join(defaults.skill, 'SKILL.md')), 'Claude default skill missing');
      assert.deepEqual(tree(defaults.skill), tree(bundledSkill), 'Claude default skill differs from package bytes');
      const defaultConfig = JSON.parse(fs.readFileSync(defaults.file, 'utf8'));
      assert.deepEqual([defaultConfig.mcpServers?.syntra?.command, ...(defaultConfig.mcpServers?.syntra?.args || [])],
        ['npx', '-y', `@synapsync/syntra-mcp@${version}`, 'mcp'], 'Claude default registration differs');
    }
    const f = fixture(host, 'success');
    const original = tree(f.home, true);
    const preview = invoke(f, host, true);
    assert.equal(preview.status, 0, `${host} preview failed: ${preview.stderr}`);
    assert.deepEqual(tree(f.home, true), original, `${host} preview wrote files`);
    assert.ok(!`${preview.stdout}${preview.stderr}`.includes('sentinel-model') && !`${preview.stdout}${preview.stderr}`.includes('\"value\": 42'), `${host} preview leaked unrelated config`);
    const applied = invoke(f, host);
    assert.equal(applied.status, 0, `${host} setup failed: ${applied.stderr}`);
    const config = fs.readFileSync(f.file, 'utf8');
    assert.ok(config.includes(host === 'codex' ? 'model = \"sentinel-model\"' : '\"sentinel\": {\"value\": 42}'), `${host} altered unrelated bytes`);
    if (host !== 'claude-code') assert.ok(config.includes('# preserved comment') || config.includes('// preserved comment'), `${host} discarded comment`);
    const pin = `@synapsync/syntra-mcp@${version}`;
    assert.equal(config.split(pin).length - 1, 1, `${host} must contain exactly one version pin`);
    assert.ok(!config.includes('@synapsync/syntra-mcp@latest'), `${host} used mutable latest`);
    if (host === 'codex') {
      assert.equal((config.match(/^\s*\[mcp_servers\.syntra\]\s*$/gm) || []).length, 1, 'Codex registration count');
      const section = config.split(/^\s*\[mcp_servers\.syntra\]\s*$/m)[1].split(/^\s*\[/m)[0];
      assert.match(section, /\bcommand\s*=\s*["']npx["']/);
      const match = section.match(/^\s*args\s*=\s*(\[[^\]\r\n]*\])\s*$/m);
      assert.ok(match, 'Codex arguments missing');
      assert.deepEqual(JSON.parse(match[1]), ['-y', pin, 'mcp']);
    } else {
      // Fixture contains only a known comment, so removing it is enough for JSON parsing here.
      const parsed = JSON.parse(config.replace(/^\s*\/\/ preserved comment\s*$/m, ''));
      const entry = host === 'claude-code' ? parsed.mcpServers?.syntra : parsed.mcp?.servers?.syntra;
      assert.ok(entry && typeof entry === 'object', `${host} registration missing`);
      const commandParts = host === 'claude-code' ? [entry.command, ...(entry.args || [])] : entry.command;
      assert.deepEqual(commandParts, ['npx', '-y', pin, 'mcp'], `${host} registration command differs`);
      if (host === 'opencode') assert.equal(entry.type, 'local');
    }
    assert.ok(fs.existsSync(path.join(f.skill, 'SKILL.md')), `${host} skill missing`);
    assert.deepEqual(tree(f.skill), tree(bundledSkill), `${host} skill differs from package bytes`);
    const backup = Object.entries(tree(f.home, true)).filter(([name, value]) => name !== path.relative(f.home, f.file) && value && value.bytes === Buffer.from(f.seed).toString('base64'));
    assert.equal(backup.length, 1, `${host} must retain one exact config backup`);
    assert.equal(backup[0][1].mode & 0o077, 0, `${host} backup is not private`);
    const once = tree(f.home, true);
    const repeated = invoke(f, host);
    assert.equal(repeated.status, 0, `${host} repeat failed: ${repeated.stderr}`);
    assert.deepEqual(tree(f.home, true), once, `${host} repeat changed files`);

    const conflict = fixture(host, 'registration-conflict');
    const conflicting = host === 'codex' ? '[mcp_servers.syntra]\ncommand = "other-command"\n' :
      host === 'claude-code' ? '{"mcpServers":{"syntra":{"command":"other-command"}}}\n' :
      '{"mcp":{"servers":{"syntra":{"type":"local","command":["other-command"]}}}}\n';
    fs.writeFileSync(conflict.file, conflicting);
    const beforeConflict = tree(conflict.home, true);
    rejected(invoke(conflict, host, true), `${host} conflicting registration preview`);
    rejected(invoke(conflict, host), `${host} conflicting registration setup`);
    assert.deepEqual(tree(conflict.home, true), beforeConflict, `${host} conflict changed files`);

    const custom = fixture(host, 'skill-conflict');
    fs.mkdirSync(custom.skill, {recursive:true, mode:0o700});
    fs.writeFileSync(path.join(custom.skill, 'SKILL.md'), 'user-modified skill\n');
    const beforeCustom = tree(custom.home, true);
    rejected(invoke(custom, host, true), `${host} modified skill preview`);
    rejected(invoke(custom, host), `${host} modified skill setup`);
    assert.deepEqual(tree(custom.home, true), beforeCustom, `${host} skill conflict changed files`);
    console.log(`PASS installed setup ${host}: dry-run, apply, skill, repeat, registration and skill conflicts`);
  }
}

async function main() {
  const { directory, candidate, digest, fromRegistry } = loadCandidate(process.argv.slice(2));
  const key = `${process.platform}/${process.arch}`;
  const target = candidate.targets[key];
  assert.ok(target, `unsupported smoke environment ${key}`);
  const [major, minor] = process.versions.node.split('.').map(Number);
  assert.ok(major > 22 || (major === 22 && minor >= 12), 'Node >=22.12 required');
  const windows = process.platform === 'win32';
  const npm = windows ? path.join(path.dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js') : findExecutable('npm');
  const runNpm = (args, options) => windows
    ? run(process.execPath, [npm, ...args], options)
    : run(npm, args, options);
  // macOS may report a temporary directory with a symlinked ancestor.
  const scratch = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'syntra candidate smoke '));
  try {
    fs.chmodSync(scratch, 0o700);
    const env = isolatedEnvironment(scratch, npm);
    if (fromRegistry) {
      env.npm_config_registry = 'https://registry.npmjs.org/';
      env.npm_config_offline = 'false';
    }
    const npmVersion = runNpm(['--version'], { cwd:scratch, env });
    assert.equal(npmVersion.status, 0, 'npm version check failed');
    const root = path.join(scratch, 'consumer'), tarballs = path.join(root, 'tarballs');
    fs.mkdirSync(tarballs, { recursive:true, mode:0o700 });
    const dependencies = { '@synapsync/syntra-mcp': candidate.version };
    if (!fromRegistry) {
      for (const name of [candidate.rootTarball, target.artifact]) fs.copyFileSync(path.join(directory, name), path.join(tarballs, name), fs.constants.COPYFILE_EXCL);
      dependencies['@synapsync/syntra-mcp'] = `file:tarballs/${candidate.rootTarball}`;
      dependencies[target.packageName] = `file:tarballs/${target.artifact}`;
    }
    fs.writeFileSync(path.join(root, 'package.json'), JSON.stringify({ name:'syntra-candidate-consumer', version:'1.0.0', private:true, dependencies }), { mode:0o600 });
    const install = runNpm(['install', '--ignore-scripts', '--include=optional', '--no-audit', '--no-fund'], { cwd:root, env, timeout:60000 });
    assert.equal(install.signal, null); assert.equal(install.status, 0, `install failed: ${install.stderr}`);
    const installedRoot = path.join(root, 'node_modules', '@synapsync', 'syntra-mcp');
    const installedTarget = path.join(root, 'node_modules', ...target.packageName.split('/'));
    const rootManifest = JSON.parse(fs.readFileSync(path.join(installedRoot, 'package.json')));
    const platformManifest = JSON.parse(fs.readFileSync(path.join(installedTarget, 'package.json')));
    assert.deepEqual(fs.readdirSync(path.dirname(installedRoot)).sort(), ['syntra-mcp', path.basename(installedTarget)].sort());
    const expectedPrivate = candidate.private ?? true;
    assert.equal(rootManifest.name, '@synapsync/syntra-mcp'); assert.equal(rootManifest.version, candidate.version); assert.equal(rootManifest.private, expectedPrivate);
    assert.deepEqual(rootManifest.optionalDependencies, Object.fromEntries(Object.values(candidate.targets).map(t => [t.packageName, candidate.version])));
    assert.equal(platformManifest.name, target.packageName); assert.equal(platformManifest.version, candidate.version); assert.equal(platformManifest.private, expectedPrivate);
    assert.equal(sha256(path.join(installedTarget, ...target.binary.split('/'))), target.binarySHA256);
    const shim = path.join(root, 'node_modules', '.bin', windows ? 'syntra.cmd' : 'syntra');
    const launcher = path.join(installedRoot, 'bin', 'launcher.cjs');
    if (windows) assert.ok(fs.statSync(shim).isFile(), 'npm did not install the Windows shim');
    else assert.equal(fs.realpathSync(shim), fs.realpathSync(launcher));
    const command = windows ? process.execPath : shim;
    const launcherArgs = windows ? [launcher] : [];
    // Syntra creates the profile with the platform's private permissions.
    const profile = path.join(scratch, 'isolated profile with spaces');
    for (const result of [run(command, [...launcherArgs, 'status', '--profile-dir', profile], {cwd:root, env}),
      runNpm(['exec', '--offline', '--yes=false', '--silent', `--package=@synapsync/syntra-mcp@${candidate.version}`, '--', 'syntra', 'status', '--profile-dir', profile], {cwd:root, env})]) {
      assert.equal(result.signal, null); assert.equal(result.status, 1); assert.equal(result.stdout, '');
      // A failing status writes its whole diagnosis to stderr: the profile in use and
      // the exact login command, with the permission flags (stdout stays empty).
      assert.match(result.stderr, /^Profile directory: ".*isolated profile with spaces" \(custom\)\n/);
      assert.match(result.stderr, /No Syntra connection\. Run: syntra login --profile-dir /);
      assert.match(result.stderr, /--prepare/);
    }
    const result = await runMCP(command, [...launcherArgs, 'mcp', '--profile-dir', profile], {cwd:root, env});
    checkMCP(result);
    const loginHint = result.frames[2].result.content.map(item => item.text || '').join(' ');
    assert.match(loginHint, /syntra login/);
    assert.match(loginHint, /syntra login --profile-dir /);
    const resolve = result.frames[1].result.tools.find(tool => tool.name === 'resolve');
    assert.ok(resolve.inputSchema.properties.team, 'MCP resolve must expose the project team selector');
    assert.deepEqual(fs.readdirSync(profile).sort(), ['.lock']);
    if (!windows && !fromRegistry) checkSetup(command, root, env, scratch, candidate.version);
    console.log(`PASS target=${key} node=${process.version} npm=${npmVersion.stdout.trim()} manifest=${digest}`);
    console.log(`PASS ${fromRegistry ? 'public registry' : 'offline tarball'} install, direct/version-pinned npm exec and MCP; no Go, Git or Syntra credentials used`);
  } finally { fs.rmSync(scratch, { recursive:true, force:true }); }
}
main().catch(error => { console.error(`FAIL candidate smoke: ${error.message}`); process.exitCode = 1; });
