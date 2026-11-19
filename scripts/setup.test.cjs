'use strict';

const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const setup = require('../bin/setup.cjs');

const root = path.resolve(__dirname, '..');
const launcherPath = path.join(root, 'bin', 'launcher.cjs');
const latestPackage = '@synapsync/syntra-mcp@latest';
const secretMarker = 'unrelated-user-setting-must-not-be-printed';

function sandbox(t) {
  const tempRoot = fs.realpathSync(os.tmpdir());
  const rootPath = fs.mkdtempSync(path.join(tempRoot, 'syntra setup preview '));
  t.after(() => fs.rmSync(rootPath, { recursive: true, force: true }));
  const home = path.join(rootPath, 'home');
  const codexHome = path.join(rootPath, 'codex-home');
  const claudeConfigDir = path.join(rootPath, 'claude-config');
  const xdgConfigHome = path.join(rootPath, 'xdg-config');
  for (const directory of [home, codexHome, claudeConfigDir, xdgConfigHome, path.join(xdgConfigHome, 'opencode')]) {
    fs.mkdirSync(directory, { recursive: true });
  }
  return {
    root: rootPath,
    home,
    codexHome,
    claudeConfigDir,
    xdgConfigHome,
    env: {
      PATH: process.env.PATH || '/usr/bin:/bin',
      HOME: home,
      USERPROFILE: home,
      CODEX_HOME: codexHome,
      CLAUDE_CONFIG_DIR: claudeConfigDir,
      XDG_CONFIG_HOME: xdgConfigHome,
      TMPDIR: rootPath,
      TEMP: rootPath,
    },
  };
}

function configFor(agent, env) {
  return setup.hostPlan(agent, env, process.platform).configPath;
}

function dryRun(agent, env) {
  return spawnSync(process.execPath, [launcherPath, 'setup', '--agent', agent, '--dry-run'], {
    cwd: root,
    env,
    encoding: 'utf8',
    timeout: 10000,
  });
}

function applyCLI(agent, env) {
  return spawnSync(process.execPath, [launcherPath, 'setup', '--agent', agent], {
    cwd: root,
    env,
    encoding: 'utf8',
    timeout: 10000,
  });
}

function writeConfig(agent, env, contents, extension) {
  let configPath = configFor(agent, env);
  if (extension) configPath = configPath.replace(/\.jsonc?$/, extension);
  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  fs.writeFileSync(configPath, contents, 'utf8');
  return configPath;
}

function jsonRegistration(agent, env) {
  return setup.buildPlan(agent, env, process.platform).targetRegistration;
}

function snapshotTree(rootPath) {
  const snapshot = [];
  const visit = (targetPath, relative = '') => {
    const stat = fs.lstatSync(targetPath);
    const mode = stat.mode & 0o777;
    if (stat.isDirectory()) {
      snapshot.push(`d\t${relative}\t${mode.toString(8)}`);
      for (const entry of fs.readdirSync(targetPath).sort()) {
        visit(path.join(targetPath, entry), relative ? `${relative}/${entry}` : entry);
      }
    } else if (stat.isFile()) {
      snapshot.push(`f\t${relative}\t${mode.toString(8)}\t${fs.readFileSync(targetPath).toString('base64')}`);
    } else if (stat.isSymbolicLink()) {
      snapshot.push(`l\t${relative}\t${fs.readlinkSync(targetPath)}`);
    }
  };
  visit(rootPath);
  return snapshot.sort().join('\n');
}

function configTextFor(agent) {
  if (agent === 'codex') return `# Keep existing settings.\r\n[profiles]\r\nname = "${secretMarker}"\r\n`;
  if (agent === 'claude-code') return `\n{\n  "model": "${secretMarker}",\n  "permissions": { "mode": "default" }\n}\n`;
  return `\n{\n  // Existing user configuration.\n  "model": "${secretMarker}",\n  "settings": { "value": true, },\n}\n`;
}

function backupFilesFor(configPath) {
  const directory = path.dirname(configPath);
  return fs.readdirSync(directory)
    .filter(name => name.includes('.syntra-backup-'))
    .map(name => path.join(directory, name));
}

test('all host previews show latest registration and skill paths without writing', t => {
  for (const agent of setup.AGENT_NAMES) {
    const fixture = sandbox(t);
    const plan = setup.buildPlan(agent, fixture.env, process.platform);
    const configPath = plan.configPath;
    const before = snapshotTree(fixture.root);
    const result = dryRun(agent, fixture.env);

    assert.equal(result.error, undefined, result.error?.message);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stderr, '');
    assert.match(result.stdout, /Syntra setup preview/);
    assert.match(result.stdout, new RegExp(latestPackage.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    assert.ok(result.stdout.includes(configPath));
    assert.ok(result.stdout.includes(plan.skillPath));
    assert.match(result.stdout, /Dry run only: no files were changed\./);
    assert.equal(fs.existsSync(configPath), false, `${agent} config should remain absent`);
    assert.equal(fs.existsSync(plan.skillPath), false, `${agent} skill should remain absent`);
    assert.equal(snapshotTree(fixture.root), before, `${agent} dry run must leave the entire fixture unchanged`);
    assert.equal(result.stdout.includes(secretMarker), false);
  }
});

test('setup validates flags before inspecting host files', t => {
  const fixture = sandbox(t);
  const invalidCalls = [
    [['--agent', 'codex', '--unknown'], /Unknown setup option/],
    [['--agent', 'codex', '--dry-run', '--dry-run'], /only once/],
    [['--agent', 'codex', '--agent', 'opencode', '--dry-run'], /only once/],
    [['--agent', 'other', '--dry-run'], /Supported setup hosts/],
    [['--dry-run'], /Choose a host/],
    [['--agent', 'codex', '--dry-run', '--api-key', secretMarker], /Unknown setup option/],
  ];

  for (const [args, message] of invalidCalls) {
    const result = spawnSync(process.execPath, [launcherPath, 'setup', ...args], {
      cwd: root,
      env: fixture.env,
      encoding: 'utf8',
      timeout: 10000,
    });
    assert.equal(result.error, undefined, result.error?.message);
    assert.equal(result.status, 1);
    assert.match(result.stderr, message);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr.includes(secretMarker), false, 'unknown argument contents must not be echoed');
  }
  assert.equal(fs.existsSync(configFor('codex', fixture.env)), false);
});

test('config roots honor environment overrides while user skill roots follow host discovery', t => {
  const fixture = sandbox(t);
  const codex = setup.hostPlan('codex', fixture.env, process.platform);
  const claude = setup.buildPlan('claude-code', fixture.env, process.platform);
  const opencode = setup.hostPlan('opencode', fixture.env, process.platform);
  assert.equal(codex.configPath, path.join(fixture.codexHome, 'config.toml'));
  assert.equal(codex.skillPath, path.join(fixture.home, '.agents', 'skills', 'syntra'));
  assert.equal(claude.configPath, path.join(fixture.claudeConfigDir, '.claude.json'));
  assert.equal(claude.skillPath, path.join(fixture.claudeConfigDir, 'skills', 'syntra'));
  assert.equal(opencode.configPath, path.join(fixture.xdgConfigHome, 'opencode', 'opencode.json'));
  assert.equal(opencode.skillPath, path.join(fixture.xdgConfigHome, 'opencode', 'skills', 'syntra'));

  const defaults = setup.hostPlan('claude-code', { HOME: fixture.home }, process.platform);
  assert.equal(defaults.configPath, path.join(fixture.home, '.claude.json'));
  assert.equal(defaults.skillPath, path.join(fixture.home, '.claude', 'skills', 'syntra'));
  const openCodeJsonc = path.join(fixture.xdgConfigHome, 'opencode', 'opencode.jsonc');
  fs.writeFileSync(openCodeJsonc, '{\n  // existing JSONC config\n  "model": "local",\n}\n');
  assert.equal(setup.hostPlan('opencode', fixture.env, process.platform).configPath, openCodeJsonc);
});

test('OpenCode custom config overrides fail closed without displaying values or writing files', t => {
  const overrideNames = ['OPENCODE_CONFIG', 'OPENCODE_CONFIG_DIR', 'OPENCODE_CONFIG_CONTENT'];
  for (const variable of overrideNames) {
    const fixture = sandbox(t);
    const sentinel = `${secretMarker}-${variable}`;
    assert.throws(
      () => setup.buildPlan('opencode', { ...fixture.env, [variable]: sentinel }, process.platform),
      error => error instanceof setup.SetupError && error.message.includes(variable) &&
        !error.message.includes(sentinel),
    );
    assert.equal(fs.existsSync(configFor('opencode', fixture.env)), false);
    assert.equal(fs.existsSync(setup.hostPlan('opencode', fixture.env, process.platform).skillPath), false);
  }
});

test('explicit setup applies all host registrations and exact skill bytes, creates a private backup, and is idempotent', t => {
  for (const agent of setup.AGENT_NAMES) {
    const fixture = sandbox(t);
    const source = configTextFor(agent);
    const configPath = writeConfig(agent, fixture.env, source, agent === 'opencode' ? '.jsonc' : undefined);
    fs.chmodSync(configPath, 0o640);

    const first = applyCLI(agent, fixture.env);
    assert.equal(first.error, undefined, first.error?.message);
    assert.equal(first.status, 0, first.stderr);
    assert.equal(first.stderr, '');
    assert.match(first.stdout, /Syntra setup complete/);
    assert.ok(first.stdout.includes(`npx -y ${latestPackage} login`));
    assert.ok(first.stdout.includes(`npx -y ${latestPackage} status`));
    assert.match(first.stdout, /share that login/);
    assert.equal(first.stdout.includes(secretMarker), false);
    assert.equal(fs.readFileSync(configPath).equals(Buffer.from(source)), false);
    const installedConfig = fs.readFileSync(configPath, 'utf8');
    assert.match(installedConfig, new RegExp(secretMarker));
    if (agent === 'claude-code') assert.ok(installedConfig.includes('"permissions": { "mode": "default" }'));
    if (agent === 'opencode') assert.ok(installedConfig.includes('"settings": { "value": true, }'));
    const installed = setup.buildPlan(agent, fixture.env, process.platform);
    assert.equal(installed.registration.status, 'matching');
    assert.equal(installed.skillStatus, 'matching');

    const backupPaths = backupFilesFor(configPath);
    assert.equal(backupPaths.length, 1);
    assert.deepEqual(fs.readFileSync(backupPaths[0]), Buffer.from(source));
    if (process.platform !== 'win32') {
      assert.equal(fs.statSync(backupPaths[0]).mode & 0o777, 0o600);
      assert.equal(fs.statSync(configPath).mode & 0o777, 0o640);
    }

    const skillPath = installed.skillPath;
    assert.deepEqual(fs.readdirSync(skillPath).sort(), ['SKILL.md', 'references']);
    assert.deepEqual(fs.readdirSync(path.join(skillPath, 'references')), ['tools.md']);
    for (const relative of ['SKILL.md', 'references/tools.md']) {
      assert.deepEqual(
        fs.readFileSync(path.join(skillPath, ...relative.split('/'))),
        fs.readFileSync(path.join(root, 'skills', 'syntra', ...relative.split('/'))),
      );
    }
    assert.equal(fs.existsSync(`${configPath}.syntra.lock`), false);
    assert.equal(fs.readdirSync(path.dirname(skillPath)).some(name => name.startsWith('.syntra-skill-stage-')), false);

    const afterFirst = snapshotTree(fixture.root);
    const second = applyCLI(agent, fixture.env);
    assert.equal(second.error, undefined, second.error?.message);
    assert.equal(second.status, 0, second.stderr);
    assert.match(second.stdout, /already match/);
    assert.equal(snapshotTree(fixture.root), afterFirst, `${agent} repeat setup must not rewrite files or backups`);
  }
});

test('setup creates a missing config privately without a backup', t => {
  const fixture = sandbox(t);
  const configPath = configFor('claude-code', fixture.env);
  const result = applyCLI('claude-code', fixture.env);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.existsSync(configPath), true);
  assert.equal(backupFilesFor(configPath).length, 0);
  if (process.platform !== 'win32') assert.equal(fs.statSync(configPath).mode & 0o777, 0o600);
  assert.equal(setup.buildPlan('claude-code', fixture.env).registration.status, 'matching');
});

test('exclusive setup lock rejects a competing attempt without overwriting the owner lock', t => {
  const fixture = sandbox(t);
  writeConfig('codex', fixture.env, configTextFor('codex'));
  let attempts = 0;
  const outer = setup.runSetup(['--agent', 'codex'], {
    env: fixture.env,
    testHooks: {
      onPhase(phase) {
        if (phase !== 'lock-acquired') return;
        attempts += 1;
        assert.throws(
          () => setup.applySetup('codex', { env: fixture.env }),
          error => error instanceof setup.SetupError && /setup lock already exists/.test(error.message),
        );
      },
    },
  });
  assert.equal(attempts, 1);
  assert.match(outer, /Syntra setup complete/);
  assert.equal(setup.buildPlan('codex', fixture.env).registration.status, 'matching');
  assert.equal(backupFilesFor(configFor('codex', fixture.env)).length, 1);
  assert.equal(fs.existsSync(`${configFor('codex', fixture.env)}.syntra.lock`), false);
});

test('apply refuses custom config, modified skill, symlink targets, and stale locks without changes', { skip: process.platform === 'win32' }, t => {
  const configFixture = sandbox(t);
  const configPath = writeConfig(
    'claude-code',
    configFixture.env,
    JSON.stringify({ mcpServers: { syntra: { type: 'stdio', command: 'different', args: [] } }, model: 'keep' }, null, 2),
  );
  const beforeConflict = snapshotTree(configFixture.root);
  assert.throws(() => setup.runSetup(['--agent', 'claude-code'], { env: configFixture.env }), /different Syntra host registration/);
  assert.equal(snapshotTree(configFixture.root), beforeConflict);

  const skillFixture = sandbox(t);
  const skillPlan = setup.hostPlan('codex', skillFixture.env, process.platform);
  fs.mkdirSync(path.join(skillPlan.skillPath, 'references'), { recursive: true });
  for (const relative of ['SKILL.md', 'references/tools.md']) {
    fs.copyFileSync(path.join(root, 'skills', 'syntra', ...relative.split('/')), path.join(skillPlan.skillPath, ...relative.split('/')));
  }
  fs.writeFileSync(path.join(skillPlan.skillPath, 'SKILL.md'), 'custom content');
  const beforeSkill = snapshotTree(skillFixture.root);
  assert.throws(() => setup.runSetup(['--agent', 'codex'], { env: skillFixture.env }), /differs from the bundled files/);
  assert.equal(snapshotTree(skillFixture.root), beforeSkill);

  const symlinkFixture = sandbox(t);
  const external = path.join(symlinkFixture.root, 'external.json');
  fs.writeFileSync(external, '{}');
  const claudeConfig = configFor('claude-code', symlinkFixture.env);
  fs.symlinkSync(external, claudeConfig);
  const beforeSymlink = snapshotTree(symlinkFixture.root);
  assert.throws(() => setup.runSetup(['--agent', 'claude-code'], { env: symlinkFixture.env }), /symbolic link/);
  assert.equal(snapshotTree(symlinkFixture.root), beforeSymlink);

  const lockFixture = sandbox(t);
  const lockConfig = writeConfig('codex', lockFixture.env, configTextFor('codex'));
  const lockPath = `${lockConfig}.syntra.lock`;
  const oldLockBytes = Buffer.from('owner data is preserved\n');
  fs.writeFileSync(lockPath, oldLockBytes, { mode: 0o600 });
  const beforeLock = snapshotTree(lockFixture.root);
  assert.throws(
    () => setup.runSetup(['--agent', 'codex'], { env: lockFixture.env }),
    error => error instanceof setup.SetupError && /remove this stale lock manually/.test(error.message),
  );
  assert.equal(snapshotTree(lockFixture.root), beforeLock);
  assert.deepEqual(fs.readFileSync(lockPath), oldLockBytes);
});

test('failure after skill install rolls back owned files and retry completes cleanly', t => {
  const fixture = sandbox(t);
  const oldText = configTextFor('codex');
  const configPath = writeConfig('codex', fixture.env, oldText);
  fs.chmodSync(configPath, 0o640);
  const plan = setup.buildPlan('codex', fixture.env, process.platform);

  assert.throws(() => setup.runSetup(['--agent', 'codex'], {
    env: fixture.env,
    testHooks: { onPhase(phase) { if (phase === 'skill-installed') throw new Error('synthetic interruption'); } },
  }), error => error instanceof setup.SetupError && /could not be completed safely/.test(error.message));

  assert.deepEqual(fs.readFileSync(configPath), Buffer.from(oldText));
  assert.equal(fs.existsSync(plan.skillPath), false);
  assert.equal(backupFilesFor(configPath).length, 0);
  assert.equal(fs.existsSync(`${configPath}.syntra.lock`), false);

  const retry = applyCLI('codex', fixture.env);
  assert.equal(retry.status, 0, retry.stderr);
  assert.equal(setup.buildPlan('codex', fixture.env).skillStatus, 'matching');
});

test('skill replacement during final validation is preserved with its backup and manual-recovery lock', t => {
  const fixture = sandbox(t);
  const original = configTextFor('claude-code');
  const configPath = writeConfig('claude-code', fixture.env, original);
  const skillPath = setup.hostPlan('claude-code', fixture.env, process.platform).skillPath;
  const skillFile = path.join(skillPath, 'SKILL.md');
  const replacement = Buffer.from(`${secretMarker}-replacement`);

  assert.throws(() => setup.runSetup(['--agent', 'claude-code'], {
    env: fixture.env,
    testHooks: {
      onPhase(phase, details) {
        if (phase !== 'skill-installed') return;
        const external = path.join(fixture.root, 'external-skill');
        fs.writeFileSync(external, replacement);
        fs.renameSync(external, path.join(details.skillPath, 'SKILL.md'));
      },
    },
  }), error => error instanceof setup.SetupError && /lock remains/.test(error.message) &&
    /remove the lock manually/.test(error.message) && !error.message.includes(secretMarker));

  assert.deepEqual(fs.readFileSync(configPath), Buffer.from(original));
  assert.deepEqual(fs.readFileSync(skillFile), replacement);
  const backupPaths = backupFilesFor(configPath);
  assert.equal(backupPaths.length, 1);
  assert.deepEqual(fs.readFileSync(backupPaths[0]), Buffer.from(original));
  if (process.platform !== 'win32') assert.equal(fs.statSync(backupPaths[0]).mode & 0o777, 0o600);
  assert.equal(fs.existsSync(`${configPath}.syntra.lock`), true);
});

test('backup mutation or replacement is preserved and blocks registration commit', t => {
  for (const behavior of ['modify', 'replace']) {
    const fixture = sandbox(t);
    const original = configTextFor('codex');
    const configPath = writeConfig('codex', fixture.env, original);
    const sentinel = Buffer.from(`${secretMarker}-${behavior}`);
    let observedBackup = null;

    assert.throws(() => setup.runSetup(['--agent', 'codex'], {
      env: fixture.env,
      testHooks: {
        onPhase(phase, details) {
          if (phase !== 'backup-created') return;
          observedBackup = details.backupPath;
          if (behavior === 'modify') {
            fs.writeFileSync(observedBackup, sentinel);
          } else {
            const replacement = `${observedBackup}.external`;
            fs.writeFileSync(replacement, sentinel, { mode: 0o600 });
            fs.renameSync(replacement, observedBackup);
          }
        },
      },
    }), error => error instanceof setup.SetupError && /lock remains/.test(error.message) &&
      !error.message.includes(secretMarker));

    assert.ok(observedBackup);
    assert.deepEqual(fs.readFileSync(configPath), Buffer.from(original));
    assert.deepEqual(fs.readFileSync(observedBackup), sentinel);
    assert.equal(fs.existsSync(setup.hostPlan('codex', fixture.env, process.platform).skillPath), false);
    assert.equal(fs.existsSync(`${configPath}.syntra.lock`), true);
    if (process.platform !== 'win32') assert.equal(fs.statSync(observedBackup).mode & 0o077, 0);
  }
});

test('concurrent config edits are preserved and trigger guarded skill rollback', t => {
  const fixture = sandbox(t);
  const oldText = configTextFor('claude-code');
  const configPath = writeConfig('claude-code', fixture.env, oldText);
  const changedText = '{\n  "external": "write won the race"\n}\n';
  const skillPath = setup.hostPlan('claude-code', fixture.env, process.platform).skillPath;

  assert.throws(() => setup.runSetup(['--agent', 'claude-code'], {
    env: fixture.env,
    testHooks: {
      onPhase(phase) {
        if (phase === 'before-config-publish') fs.writeFileSync(configPath, changedText);
      },
    },
  }), error => error instanceof setup.SetupError && /changed during setup/.test(error.message));

  assert.equal(fs.readFileSync(configPath, 'utf8'), changedText);
  assert.equal(fs.existsSync(skillPath), false);
  assert.equal(backupFilesFor(configPath).length, 0);
  assert.equal(fs.existsSync(`${configPath}.syntra.lock`), false);
});

test('new config publication cannot clobber a file that appears after validation', t => {
  const fixture = sandbox(t);
  const configPath = configFor('opencode', fixture.env);
  const winner = '{\n  "manual": "created concurrently"\n}\n';
  const skillPath = setup.hostPlan('opencode', fixture.env, process.platform).skillPath;

  assert.throws(() => setup.runSetup(['--agent', 'opencode'], {
    env: fixture.env,
    testHooks: {
      onPhase(phase) {
        if (phase === 'config-validated') fs.writeFileSync(configPath, winner, { flag: 'wx' });
      },
    },
  }), error => error instanceof setup.SetupError && /could not be completed safely/.test(error.message));

  assert.equal(fs.readFileSync(configPath, 'utf8'), winner);
  assert.equal(fs.existsSync(skillPath), false);
  assert.equal(backupFilesFor(configPath).length, 0);
  assert.equal(fs.existsSync(`${configPath}.syntra.lock`), false);
});

test('JSON and JSONC insertion preserves unrelated inline bytes, comments, commas, and CRLF', t => {
  const fixture = sandbox(t);
  const cases = [
    {
      agent: 'claude-code',
      source: '{\r\n  "sentinel": {"value": 42},\r\n  "permissions": {"allow": ["read"]}\r\n}\r\n',
      preserved: ['"sentinel": {"value": 42}', '"permissions": {"allow": ["read"]}'],
    },
    {
      agent: 'opencode',
      source: '{\r\n  // Keep this comment with the existing field.\r\n  "sentinel": {"value": 42},\r\n  "mcp": { "servers": { "other": {"value": 1}, }, },\r\n}\r\n',
      preserved: ['// Keep this comment with the existing field.', '"sentinel": {"value": 42}', '"other": {"value": 1}'],
    },
  ];

  for (const entry of cases) {
    const plan = setup.buildPlan(entry.agent, fixture.env, process.platform);
    const candidate = setup.computeJsonCandidate(
      entry.source,
      plan.registrationPath,
      plan.targetRegistration,
      plan.format,
    );
    for (const text of entry.preserved) assert.ok(candidate.includes(text), `${entry.agent} should preserve ${text}`);
    assert.equal(candidate.replace(/\r\n/g, '').includes('\n'), false, `${entry.agent} insertions use CRLF only`);

    const configPath = writeConfig(entry.agent, fixture.env, candidate, entry.agent === 'opencode' ? '.jsonc' : undefined);
    assert.equal(setup.buildPlan(entry.agent, fixture.env).registration.status, 'matching');
    fs.unlinkSync(configPath);
  }

  const commentOnly = '{\n  "mcp": { "servers": { /* retain object comment */ } }\n}\n';
  const openCode = setup.buildPlan('opencode', fixture.env, process.platform);
  const withComment = setup.computeJsonCandidate(commentOnly, openCode.registrationPath, openCode.targetRegistration, openCode.format);
  assert.ok(withComment.includes('/* retain object comment */'));
  const commentPath = writeConfig('opencode', fixture.env, withComment, '.jsonc');
  assert.equal(setup.buildPlan('opencode', fixture.env).registration.status, 'matching');
  fs.unlinkSync(commentPath);
  const claude = setup.buildPlan('claude-code', fixture.env, process.platform);
  const emptyCandidate = setup.computeJsonCandidate('', claude.registrationPath, claude.targetRegistration, claude.format);
  assert.deepEqual(JSON.parse(emptyCandidate).mcpServers.syntra, claude.targetRegistration);
});

test('Codex TOML appends only the Syntra table and preserves all existing bytes', t => {
  const fixture = sandbox(t);
  const config = '# Keep this comment byte-for-byte.\n[profiles]\nname = "hidden-existing-value"';
  const configPath = writeConfig('codex', fixture.env, config);
  const plan = setup.buildPlan('codex', fixture.env, process.platform);
  const candidate = setup.computeTomlCandidate(config, plan.tomlSnippet);

  assert.equal(candidate.slice(0, config.length), config);
  assert.match(candidate, /\[mcp_servers\.syntra\]/);
  assert.ok(candidate.includes(`args = ["--yes", "--prefer-online", "${latestPackage}", "mcp"]`));
  assert.equal(fs.readFileSync(configPath, 'utf8'), config);
  const output = setup.renderPlan(plan);
  assert.match(output, /Syntra MCP registration (?:addition|migration)/);
  assert.equal(output.includes('hidden-existing-value'), false);
});

test('Codex TOML addition follows a consistent CRLF file without rewriting its prefix', t => {
  const fixture = sandbox(t);
  const config = '# keep this line\r\n[profiles]\r\nname = "private"';
  const plan = setup.buildPlan('codex', fixture.env, process.platform);
  const candidate = setup.computeTomlCandidate(config, plan.tomlSnippet);
  const addition = candidate.slice(config.length);

  assert.equal(candidate.slice(0, config.length), config);
  assert.match(addition, /^\r\n\r\n\[mcp_servers\.syntra\]\r\n/);
  assert.equal(addition.replace(/\r\n/g, '').includes('\n'), false);
});

test('Codex recognizes equivalent table, quoted table, and dotted-key TOML declarations', t => {
  const fixture = sandbox(t);
  const registration = setup.buildPlan('codex', fixture.env, process.platform).targetRegistration;
  const args = JSON.stringify(registration.args);
  const declarations = [
    `[mcp_servers.syntra]\ncommand = "${registration.command}"\nargs = ${args}\n`,
    `[mcp_servers."syntra"]\ncommand = "${registration.command}"\nargs = ${args}\n`,
    `mcp_servers.syntra = { command = "${registration.command}", args = ${args} }\n`,
  ];

  for (const source of declarations) {
    const configPath = writeConfig('codex', fixture.env, source);
    const plan = setup.buildPlan('codex', fixture.env, process.platform);
    assert.equal(plan.registration.status, 'matching');
    assert.equal(fs.readFileSync(configPath, 'utf8'), source);
  }
});

test('Claude strict JSON preview preserves unrelated values and displays only the new registration', t => {
  const fixture = sandbox(t);
  const source = `${JSON.stringify({ model: secretMarker, custom: { retain: true }, mcpServers: {} }, null, 2)}\n`;
  const configPath = writeConfig('claude-code', fixture.env, source);
  const plan = setup.buildPlan('claude-code', fixture.env, process.platform);
  const candidate = setup.computeJsonCandidate(source, plan.registrationPath, plan.targetRegistration, 'json');
  const parsed = JSON.parse(candidate);

  assert.deepEqual(parsed.model, secretMarker);
  assert.deepEqual(parsed.custom, { retain: true });
  assert.deepEqual(parsed.mcpServers.syntra, plan.targetRegistration);
  assert.equal(fs.readFileSync(configPath, 'utf8'), source);
  const output = setup.renderPlan(plan);
  assert.match(output, /mcpServers\.syntra/);
  assert.equal(output.includes(secretMarker), false);
  assert.equal(output.includes(JSON.stringify(parsed)), false);
});

test('OpenCode JSONC preview preserves comments, spacing and trailing commas', t => {
  const fixture = sandbox(t);
  const configPath = path.join(fixture.xdgConfigHome, 'opencode', 'opencode.jsonc');
  const source = [
    '{',
    '  // retain this comment and exact spacing',
    `  "model": "${secretMarker}",`,
    '  "mcp": {',
    '    "servers": {},',
    '  },',
    '}',
    '',
  ].join('\n');
  fs.writeFileSync(configPath, source, 'utf8');
  const plan = setup.buildPlan('opencode', fixture.env, process.platform);
  const candidate = setup.computeJsonCandidate(source, plan.registrationPath, plan.targetRegistration, 'jsonc');
  const parsed = require('../bin/vendor/jsonc-parser/lib/umd/main.js').parse(candidate, [], {
    allowTrailingComma: true,
    disallowComments: false,
  });

  assert.equal(parsed.model, secretMarker);
  assert.deepEqual(parsed.mcp.servers.syntra, plan.targetRegistration);
  assert.ok(candidate.includes('  // retain this comment and exact spacing\n'));
  assert.ok(candidate.includes(`  "model": "${secretMarker}",\n`));
  assert.equal(fs.readFileSync(configPath, 'utf8'), source);
  assert.equal(setup.renderPlan(plan).includes(secretMarker), false);
});

test('exact existing registrations are idempotent and conflicting registrations are refused', t => {
  for (const agent of ['claude-code', 'opencode']) {
    const fixture = sandbox(t);
    const registration = jsonRegistration(agent, fixture.env);
    const plan = setup.hostPlan(agent, fixture.env, process.platform);
    const contents = { [plan.registrationPath[0]]: {} };
    contents.model = secretMarker;
    let target = contents;
    for (const segment of plan.registrationPath.slice(0, -1)) target = target[segment] || (target[segment] = {});
    target[plan.registrationPath.at(-1)] = registration;
    const configPath = writeConfig(agent, fixture.env, `${JSON.stringify(contents, null, 2)}\n`);
    const exactPlan = setup.buildPlan(agent, fixture.env, process.platform);
    assert.equal(exactPlan.registration.status, 'matching');
    assert.match(setup.renderPlan(exactPlan), /MCP registration preserved/);
    const exactBytes = fs.readFileSync(configPath);

    target[plan.registrationPath.at(-1)] = { ...registration, command: 'different-command' };
    const conflictBytes = `${JSON.stringify(contents, null, 2)}\n`;
    fs.writeFileSync(configPath, conflictBytes);
    assert.throws(
      () => setup.buildPlan(agent, fixture.env, process.platform),
      error => error instanceof setup.SetupError && /different Syntra host registration/.test(error.message) &&
        !error.message.includes(secretMarker),
    );
    assert.notDeepEqual(fs.readFileSync(configPath), exactBytes);
    assert.equal(fs.readFileSync(configPath, 'utf8'), conflictBytes);
  }

  const fixture = sandbox(t);
  const conflictToml = `[mcp_servers.syntra]\ncommand = "different-command"\nargs = ["-y", "${latestPackage}", "mcp"]\n`;
  const configPath = writeConfig('codex', fixture.env, conflictToml);
  assert.throws(() => setup.buildPlan('codex', fixture.env), /different Syntra host registration/);
  assert.equal(fs.readFileSync(configPath, 'utf8'), conflictToml);
});

test('malformed configs, duplicate keys, and competing OpenCode files fail without exposing contents', t => {
  const fixture = sandbox(t);
  const malformedCodex = writeConfig('codex', fixture.env, `name = "${secretMarker}\n`);
  assert.throws(() => setup.buildPlan('codex', fixture.env), /TOML host config is malformed/);
  assert.equal(fs.readFileSync(malformedCodex, 'utf8'), `name = "${secretMarker}\n`);

  const malformedClaude = writeConfig('claude-code', fixture.env, `{"profile":"${secretMarker}",}`);
  assert.throws(() => setup.buildPlan('claude-code', fixture.env), /JSON host config is malformed/);
  assert.equal(fs.readFileSync(malformedClaude, 'utf8'), `{"profile":"${secretMarker}",}`);
  const duplicateClaude = writeConfig('claude-code', fixture.env, '{"model":"safe","model":"hidden"}');
  assert.throws(() => setup.buildPlan('claude-code', fixture.env), /duplicate object keys/);

  const openCodeJson = path.join(fixture.xdgConfigHome, 'opencode', 'opencode.json');
  const openCodeJsonc = path.join(fixture.xdgConfigHome, 'opencode', 'opencode.jsonc');
  fs.writeFileSync(openCodeJson, '{}');
  fs.writeFileSync(openCodeJsonc, '{}');
  assert.throws(() => setup.buildPlan('opencode', fixture.env), /both global JSON and JSONC/);
  assert.equal(fs.readFileSync(duplicateClaude, 'utf8'), '{"model":"safe","model":"hidden"}');
  assert.equal(fs.readFileSync(openCodeJson, 'utf8'), '{}');
  assert.equal(fs.readFileSync(openCodeJsonc, 'utf8'), '{}');
});

test('host configs with invalid UTF-8 fail closed without rewriting or exposing bytes', t => {
  for (const agent of setup.AGENT_NAMES) {
    const fixture = sandbox(t);
    const configPath = configFor(agent, fixture.env);
    fs.mkdirSync(path.dirname(configPath), { recursive: true });
    const bytes = Buffer.from([0x7b, 0x22, 0x6b, 0x22, 0x3a, 0x22, 0xff, 0x22, 0x7d]);
    fs.writeFileSync(configPath, bytes);

    assert.throws(
      () => setup.buildPlan(agent, fixture.env, process.platform),
      error => error instanceof setup.SetupError && /valid UTF-8|lossless UTF-8/.test(error.message) &&
        !error.message.includes('\ufffd') && !error.message.includes(secretMarker),
      `${agent} should reject invalid UTF-8 safely`,
    );
    assert.deepEqual(fs.readFileSync(configPath), bytes, `${agent} bytes should remain unchanged`);
  }
});

test('a UTF-8 BOM is rejected explicitly without modifying the host config', t => {
  const fixture = sandbox(t);
  const configPath = configFor('claude-code', fixture.env);
  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  const bytes = Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from('{"model":"unchanged"}')]);
  fs.writeFileSync(configPath, bytes);
  assert.throws(() => setup.buildPlan('claude-code', fixture.env), /JSON host config is malformed/);
  assert.deepEqual(fs.readFileSync(configPath), bytes);
});

test('existing bundled skills are recognized; modified and extra-content skills are preserved and refused', t => {
  const fixture = sandbox(t);
  const plan = setup.buildPlan('codex', fixture.env, process.platform);
  const sourceSkill = path.join(root, 'skills', 'syntra');
  fs.mkdirSync(path.join(plan.skillPath, 'references'), { recursive: true });
  for (const relative of ['SKILL.md', 'references/tools.md']) {
    fs.copyFileSync(path.join(sourceSkill, ...relative.split('/')), path.join(plan.skillPath, ...relative.split('/')));
  }
  assert.equal(setup.buildPlan('codex', fixture.env).skillStatus, 'matching');

  const skillFile = path.join(plan.skillPath, 'SKILL.md');
  fs.writeFileSync(skillFile, secretMarker);
  assert.throws(
    () => setup.buildPlan('codex', fixture.env),
    error => error instanceof setup.SetupError && /differs from the bundled files/.test(error.message) &&
      !error.message.includes(secretMarker),
  );
  assert.equal(fs.readFileSync(skillFile, 'utf8'), secretMarker);
  fs.copyFileSync(path.join(sourceSkill, 'SKILL.md'), skillFile);
  fs.writeFileSync(path.join(plan.skillPath, 'unrelated.txt'), secretMarker);
  assert.throws(() => setup.buildPlan('codex', fixture.env), /differs from the bundled files/);
  assert.equal(fs.readFileSync(path.join(plan.skillPath, 'unrelated.txt'), 'utf8'), secretMarker);
});

test('symbolic links, hard-linked configs, relative overrides, and unsafe skill paths are refused', { skip: process.platform === 'win32' }, t => {
  const fixture = sandbox(t);
  const sourceFile = path.join(fixture.root, 'external-config');
  fs.writeFileSync(sourceFile, '{}');
  const claudeConfig = configFor('claude-code', fixture.env);
  fs.symlinkSync(sourceFile, claudeConfig);
  assert.throws(() => setup.buildPlan('claude-code', fixture.env), /symbolic link/);
  fs.unlinkSync(claudeConfig);

  const linkedHome = path.join(fixture.root, 'linked-codex-home');
  fs.symlinkSync(fixture.codexHome, linkedHome);
  const linkedEnv = { ...fixture.env, CODEX_HOME: linkedHome };
  assert.throws(() => setup.buildPlan('codex', linkedEnv), /symbolic link/);

  const openPlan = setup.hostPlan('opencode', fixture.env, process.platform);
  const skillParent = path.dirname(openPlan.skillPath);
  const outside = path.join(fixture.root, 'outside-skill');
  fs.mkdirSync(outside);
  fs.symlinkSync(outside, skillParent);
  assert.throws(() => setup.buildPlan('opencode', fixture.env), /symbolic link/);

  const hardlinkFixture = sandbox(t);
  const linkedCodexConfig = configFor('codex', hardlinkFixture.env);
  const original = path.join(hardlinkFixture.root, 'shared-file');
  fs.writeFileSync(original, '');
  fs.linkSync(original, linkedCodexConfig);
  assert.throws(() => setup.buildPlan('codex', hardlinkFixture.env), /multiple hard links/);
  assert.throws(
    () => setup.buildPlan('codex', { ...fixture.env, CODEX_HOME: 'relative/path' }),
    /must be an absolute path/,
  );
});

test('Windows latest launch uses a Node argv array instead of cmd.exe', () => {
  const node = 'C:\\Program Files\\nodejs\\node.exe';
  const npxCli = 'C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npx-cli.js';
  const command = setup.shellCommand('win32', 'latest', { node, npxCli });
  assert.equal(command.command, node);
  assert.deepEqual(command.args, [npxCli, '--yes', '--prefer-online', latestPackage, 'mcp']);
  const plan = setup.hostPlan('claude-code', {
    USERPROFILE: 'C:\\Users\\fixture',
    CLAUDE_CONFIG_DIR: 'C:\\Users\\fixture\\claude-data',
  }, 'win32');
  assert.equal(plan.configPath, 'C:\\Users\\fixture\\claude-data\\.claude.json');
  assert.equal(plan.skillPath, 'C:\\Users\\fixture\\claude-data\\skills\\syntra');
});

test('the setup dispatch bypasses native binary lookup while existing launcher tests own normal delegation', t => {
  const fixture = sandbox(t);
  const result = dryRun('opencode', fixture.env);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Syntra setup preview for OpenCode/);
  assert.equal(result.stderr, '');
  assert.equal(result.stdout.includes('binary for'), false);
});

test('setup lock errors never echo control characters from a configured path', { skip: process.platform === 'win32' }, t => {
  const fixture = sandbox(t);
  const unsafeHome = path.join(fixture.root, '\u001b[31munsafe');
  fs.mkdirSync(unsafeHome, { recursive: true });
  fixture.env.HOME = unsafeHome;
  delete fixture.env.XDG_CONFIG_HOME;
  const config = writeConfig('codex', fixture.env, configTextFor('codex'));
  fs.writeFileSync(`${config}.syntra.lock`, 'lock', { mode: 0o600 });
  assert.throws(() => setup.runSetup(['--agent', 'codex'], { env: fixture.env }), error =>
    error instanceof setup.SetupError && /setup lock already exists/.test(error.message) && !error.message.includes('\u001b'));
});

test('recognizes and previews stale Syntra registrations, then replaces them with a private backup', t => {
  for (const agent of setup.AGENT_NAMES) {
    const fixture = sandbox(t);
    const plan = setup.hostPlan(agent, fixture.env, process.platform);
    const before = agent === 'codex'
      ? `model = "keep"\r\n[mcp_servers."syntra"]\r\ncommand = "syntra" # old executable\r\nargs = [\r\n  "mcp", "--profile-dir", "${secretMarker}"\r\n]\r\n[mcp_servers.other]\r\ncommand = "keep"\r\n`
      : agent === 'claude-code'
        ? `{"model":"keep","mcpServers":{"syntra":{"type":"stdio","command":"npx","args":["--yes","@synapsync/syntra-mcp@0.1.1","mcp","--call-timeout=1m"],"env":{"PRIVATE":"${secretMarker}"}},"other":{"command":"keep"}}}\n`
        : `{ // preserve this line\n  "mcp": {"servers": {"syntra":{"type":"local","command":["npx","-y","@synapsync/syntra-mcp@0.1.1","mcp","--profile-dir=custom"]}, "other":{"command":["keep"]},}},\n}\n`;
    const configPath = writeConfig(agent, fixture.env, before);
    fs.mkdirSync(plan.skillPath, { recursive: true });
    fs.writeFileSync(path.join(plan.skillPath, 'SKILL.md'), 'existing skill');
    const invoke = (...extra) => spawnSync(process.execPath, [launcherPath, 'setup', '--agent', agent, ...extra], {
      env: fixture.env, encoding: 'utf8', timeout: 10000,
    });
    const preserved = invoke();
    assert.equal(preserved.status, 0, preserved.stderr);
    assert.equal(fs.readFileSync(configPath, 'utf8'), before);
    const preview = invoke('--mode', 'latest', '--dry-run');
    assert.equal(preview.status, 0, preview.stderr);
    assert.match(preview.stdout, /Current mode: (?:direct|pinned @0.1.1)/);
    assert.ok(preview.stdout.includes(latestPackage));
    assert.ok(!preview.stdout.includes(secretMarker));
    assert.equal(fs.readFileSync(configPath, 'utf8'), before);

    const refused = invoke('--mode', 'latest');
    assert.equal(refused.status, 1);
    assert.ok(refused.stderr.includes(`syntra setup --agent ${agent} --mode latest --replace`));
    assert.match(refused.stdout, /Syntra MCP registration (?:addition|migration)/);
    assert.equal(fs.readFileSync(configPath, 'utf8'), before);

    const applied = invoke('--mode', 'latest', '--replace');
    assert.equal(applied.status, 0, applied.stderr);
    assert.match(applied.stdout, /MCP registration updated/);
    const backupPath = applied.stdout.match(/Private config backup: (.+)/)[1];
    assert.equal(fs.readFileSync(backupPath, 'utf8'), before);
    if (process.platform !== 'win32') assert.equal(fs.statSync(backupPath).mode & 0o777, 0o600);
    const after = fs.readFileSync(configPath, 'utf8');
    assert.ok(after.includes(latestPackage));
    if (agent !== 'opencode') assert.ok(after.includes(secretMarker));
    assert.ok(after.includes(agent === 'claude-code' ? '--call-timeout=1m' : '--profile-dir'));
    assert.ok(after.includes('keep'));
    if (agent === 'codex') assert.ok(after.includes('[mcp_servers.other]\r\ncommand = "keep"\r\n'));
    if (agent === 'opencode') assert.ok(after.includes('// preserve this line'));
    assert.equal(fs.readFileSync(path.join(plan.skillPath, 'SKILL.md'), 'utf8'), 'existing skill');
    assert.equal(setup.registrationState(agent, fixture.env).mode, 'latest');
  }
});

test('Codex replaces dotted and inline TOML registrations without touching neighboring values', t => {
  for (const entry of [
    'mcp_servers.syntra.command = "syntra"\nmcp_servers.syntra.args = ["mcp"]\n',
    'mcp_servers.syntra = { command = "syntra", args = ["mcp"] }\n',
    '[mcp_servers]\nsyntra.command = "syntra"\nsyntra.args = ["mcp"]\n',
  ]) {
    const fixture = sandbox(t);
    const source = `# outside\n${entry}\n[mcp_servers.other]\ncommand = "keep"\n`;
    const configPath = writeConfig('codex', fixture.env, source);
    const output = setup.runSetup(['--agent', 'codex', '--mode', 'latest', '--replace'], { env: fixture.env });
    assert.match(output, /MCP registration updated/);
    const after = fs.readFileSync(configPath, 'utf8');
    assert.ok(after.includes('[mcp_servers.other]\ncommand = "keep"\n'));
    assert.ok(after.includes(latestPackage));
    assert.ok(after.startsWith('# outside\n'));
  }
});

test('replacement confirmation is bound to the reviewed snapshot and leaves unknown registrations alone', async t => {
  const fixture = sandbox(t);
  const source = '[mcp_servers.syntra]\ncommand = "syntra"\nargs = ["mcp"]\n';
  const configPath = writeConfig('codex', fixture.env, source);
  const output = [];
  const options = { env: fixture.env, tty: true, out: line => output.push(line) };
  await setup.setupCLI(['--agent', 'codex', '--mode', 'latest'], { ...options, prompt: async () => 'no' });
  assert.match(output.join(''), /Setup cancelled/);
  assert.equal(fs.readFileSync(configPath, 'utf8'), source);

  const changed = source + '# user edit\n';
  await assert.rejects(setup.setupCLI(['--agent', 'codex', '--mode', 'latest'], {
    ...options, prompt: async () => { fs.writeFileSync(configPath, changed); return 'yes'; },
  }), /changed after the setup preview/);
  assert.equal(fs.readFileSync(configPath, 'utf8'), changed);
  await setup.setupCLI(['--agent', 'codex', '--mode', 'latest'], { ...options, prompt: async () => 'yes' });
  assert.match(fs.readFileSync(configPath, 'utf8'), /@synapsync\/syntra-mcp@/);

  const unknown = `[mcp_servers.syntra]\ncommand = "unrelated"\nargs = ["${latestPackage}", "mcp"]\n`;
  fs.writeFileSync(configPath, unknown);
  assert.throws(() => setup.runSetup(['--agent', 'codex', '--mode', 'latest', '--replace'], { env: fixture.env }), /could not be identified/);
  assert.equal(fs.readFileSync(configPath, 'utf8'), unknown);
});

test('replaces the Syntra value inside a shared TOML inline table', t => {
  const fixture = sandbox(t);
  const source = 'mcp_servers = { syntra = { command = "syntra", args = ["mcp"] }, other = {command="keep"} }\n';
  const configPath = writeConfig('codex', fixture.env, source);
  assert.match(setup.runSetup(['--agent', 'codex', '--mode', 'latest', '--replace'], { env: fixture.env }), /MCP registration updated/);
  const after = fs.readFileSync(configPath, 'utf8');
  assert.ok(after.startsWith('mcp_servers = { syntra = { command = '));
  assert.ok(after.endsWith(', other = {command="keep"} }\n'));
  assert.ok(after.includes(latestPackage));
});

test('retains the existing OpenCode layout when refreshing a legacy V1 entry', t => {
  const fixture = sandbox(t);
  const source = '{"mcp":{"syntra":{"type":"local","command":["npx","-y","@synapsync/syntra-mcp@0.1.1","mcp"]},"other":{"command":["keep"]}}}';
  const configPath = writeConfig('opencode', fixture.env, source);
  assert.equal(setup.registrationState('opencode', fixture.env).pin, '0.1.1');
  assert.match(setup.runSetup(['--agent', 'opencode', '--mode', 'latest', '--replace'], { env: fixture.env }), /MCP registration updated/);
  const { mcp } = JSON.parse(fs.readFileSync(configPath, 'utf8'));
  assert.ok(mcp.syntra.command.includes(latestPackage));
  assert.deepEqual(mcp.other, { command: ['keep'] });
  assert.equal(mcp.servers, undefined);
});

test('Codex changes only launch fields and preserves nested tables and spacing', t => {
  for (const eol of ['\n', '\r\n']) {
    const fixture = sandbox(t);
    const plan = setup.buildPlan('codex', fixture.env, process.platform);
    const before = [
      'model = "keep"', '', '[mcp_servers.other]', 'command = "keep"', '',
      '[mcp_servers.syntra]', 'command = "syntra"',
      'args = [', '  "mcp", "--profile-dir", "/fixture/profile"', ']',
      '', '[mcp_servers.syntra.env]', 'DEPRECATED = "remove"', '', '',
      '[projects."/x"]', 'trust_level = "trusted"', '', '',
    ].join(eol);
    const configPath = writeConfig('codex', fixture.env, before);
    const result = spawnSync(process.execPath, [launcherPath, 'setup', '--agent', 'codex', '--mode', 'latest', '--replace'], {
      env: fixture.env, encoding: 'utf8', timeout: 10000,
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(fs.readFileSync(configPath, 'utf8'), [
      'model = "keep"', '', '[mcp_servers.other]', 'command = "keep"', '',
      '[mcp_servers.syntra]', `command = ${JSON.stringify(plan.targetRegistration.command)}`,
      `args = [${[...plan.targetRegistration.args, '--profile-dir', '/fixture/profile'].map(arg => JSON.stringify(arg)).join(', ')}]`,
      '', '[mcp_servers.syntra.env]', 'DEPRECATED = "remove"', '', '',
      '[projects."/x"]', 'trust_level = "trusted"', '', '',
    ].join(eol));
  }
});

test('Codex append keeps one separator and a single terminal newline', t => {
  const fixture = sandbox(t);
  const plan = setup.buildPlan('codex', fixture.env, process.platform);
  const base = '[projects."/x"]\ntrust_level = "trusted"';
  for (const end of ['', '\n', '\n\n', '\n  \n\n']) {
    assert.equal(setup.computeTomlCandidate(base + end, plan.tomlSnippet), base + '\n\n' + plan.tomlSnippet);
  }
});

test('JSON host replacements edit only launch values and retain surrounding formatting', t => {
  for (const agent of ['claude-code', 'opencode']) {
    const fixture = sandbox(t);
    const plan = setup.buildPlan(agent, fixture.env, process.platform);
    const legacy = agent === 'claude-code'
      ? { type: 'stdio', command: 'syntra', args: ['mcp'] }
      : { type: 'local', command: ['syntra', 'mcp'] };
    const tree = registration => {
      let config = { first: 'keep', syntra: registration, last: 'keep' };
      for (const segment of [...plan.registrationPath.slice(0, -1)].reverse()) {
        config = { first: 'keep', [segment]: config, last: 'keep' };
      }
      return JSON.stringify(config, null, 2) + '\n';
    };
    const configPath = writeConfig(agent, fixture.env, tree('inline-stale').replace('"inline-stale"', JSON.stringify(legacy)) + '\n');
    assert.match(setup.runSetup(['--agent', agent, '--mode', 'latest', '--replace'], { env: fixture.env }), /MCP registration updated/);
    assert.equal(fs.readFileSync(configPath, 'utf8'), tree('inline-stale').replace('"inline-stale"', JSON.stringify(plan.targetRegistration)) + '\n');
  }
});

test('local setup binds every host to the verified npm global launcher and keeps that path across version changes', t => {
  for (const agent of setup.AGENT_NAMES) {
    const fixture = sandbox(t);
    const prefix = path.join(fixture.root, 'selected global prefix');
    const modules = path.join(prefix, ...(process.platform === 'win32' ? [] : ['lib']), 'node_modules');
    const packageDir = path.join(modules, '@synapsync', 'syntra-mcp');
    const launcher = path.join(packageDir, 'bin', 'launcher.cjs');
    fs.mkdirSync(path.dirname(launcher), { recursive: true });
    fs.writeFileSync(launcher, '/* fake installed version 1 */');
    const calls = [];
    const opts = { env: { ...fixture.env, PATH: '' }, packageDir, npm: args => {
      calls.push(args);
      return { status: 0, stdout: modules };
    } };
    const args = ['--agent', agent, '--mode', 'local'];
    const preview = setup.runSetup([...args, '--dry-run'], opts);
    assert.match(preview, /Proposed mode: local/);
    assert.ok(preview.includes(JSON.stringify(launcher)));
    const config = configFor(agent, fixture.env);
    assert.equal(fs.existsSync(config), false);
    assert.match(setup.runSetup(args, opts), /MCP registration updated/);
    const registered = fs.readFileSync(config, 'utf8');
    assert.deepEqual(setup.registrationState(agent, fixture.env, process.platform, packageDir), { mode: 'local', target: 'current' });
    fs.writeFileSync(launcher, '/* fake installed version 2 */');
    assert.match(setup.runSetup(args, opts), /No files were changed/);
    assert.equal(fs.readFileSync(config, 'utf8'), registered);
    assert.ok(calls.length >= 3);
    assert.ok(calls.every(call => JSON.stringify(call) === JSON.stringify(['root', '-g', '--prefix', prefix])));
    fs.unlinkSync(launcher);
    assert.deepEqual(setup.registrationState(agent, fixture.env, process.platform, packageDir), { mode: 'local', target: 'unresolved' });
    assert.throws(() => setup.runSetup(args, opts), /requires running setup from the selected npm-global installation/);
    assert.equal(fs.readFileSync(config, 'utf8'), registered);
  }
});

test('local setup fails without a verified global installation and unknown wrappers require manual resolution', t => {
  const fixture = sandbox(t);
  assert.throws(() => setup.runSetup(['--agent', 'codex', '--mode', 'local'], {
    env: fixture.env,
  }), /requires running setup from the selected npm-global installation/);
  assert.equal(fs.existsSync(configFor('codex', fixture.env)), false);
  const custom = '[mcp_servers.syntra]\ncommand = "/custom/bin/syntra"\nargs = ["mcp"]\n';
  const config = writeConfig('codex', fixture.env, custom);
  assert.throws(() => setup.runSetup(['--agent', 'codex', '--mode', 'latest', '--replace'], {
    env: fixture.env,
  }), /could not be identified/);
  assert.equal(fs.readFileSync(config, 'utf8'), custom);
});
