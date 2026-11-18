'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const readline = require('node:readline/promises');

const PACKAGE = '@synapsync/syntra-mcp';
const MANUAL = `npm install -g ${PACKAGE}@latest`;
const STABLE = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;

function compare(a, b) {
  const left = a.split('.').map(BigInt);
  const right = b.split('.').map(BigInt);
  for (let i = 0; i < 3; i++) if (left[i] !== right[i]) return left[i] > right[i] ? 1 : -1;
  return 0;
}

// cmd.exe is required to launch npm.cmd on Windows; only fixed tokens and a
// validated numeric version reach it. No user or registry text is shell code.
function npm(args, platform = process.platform) {
  const command = platform === 'win32' ? 'cmd.exe' : 'npm';
  const argv = platform === 'win32' ? ['/d', '/c', 'npm.cmd', ...args] : args;
  return spawnSync(command, argv, { shell: false, encoding: 'utf8', timeout: 30000, maxBuffer: 1024 * 1024, windowsHide: true });
}

function reportPinnedHosts(latest, env, out) {
  const { AGENT_NAMES, registrationPin } = require('./setup.cjs');
  for (const agent of AGENT_NAMES) {
    try {
      const pin = registrationPin(agent, env);
      if (pin && STABLE.test(pin) && compare(pin, latest) < 0) {
        out(`Syntra for ${agent} is pinned to ${pin}. Run: syntra setup --agent ${agent} --replace`);
      }
    } catch {
      out(`Could not inspect the ${agent} registration. Preview it with: syntra setup --agent ${agent} --dry-run`);
    }
  }
}

function installation(packageDir, globalRoot) {
  const real = fs.realpathSync(packageDir);
  if (real.split(/[\\/]+/).some(part => part.toLowerCase() === '_npx')) return 'npx';
  try {
    const expected = fs.realpathSync(path.join(globalRoot, '@synapsync', 'syntra-mcp'));
    return real === expected ? 'global' : 'local';
  } catch { return 'local'; }
}

async function update(args, deps = {}) {
  const out = deps.out || (message => process.stdout.write(message + '\n'));
  const err = deps.err || (message => process.stderr.write(`syntra: ${message}\n`));
  const call = deps.npm || npm;
  const packageDir = deps.packageDir || path.resolve(__dirname, '..');
  const env = deps.env || process.env;
  const readVersion = deps.readVersion || (() => JSON.parse(fs.readFileSync(path.join(packageDir, 'package.json'), 'utf8')).version);
  const prompt = deps.prompt || (async () => {
    const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
    try { return await rl.question('Continue? [y/N] '); } finally { rl.close(); }
  });
  const tty = deps.tty ?? Boolean(process.stdin.isTTY && process.stdout.isTTY);
  if (args.some(a => !['--check', '--yes'].includes(a)) || new Set(args).size !== args.length || (args.includes('--check') && args.includes('--yes'))) {
    err('Usage: syntra update [--check | --yes]'); return 1;
  }
  if (env.SYNTRA_UPDATE_OFFLINE === '1') { err('Update checks are disabled by SYNTRA_UPDATE_OFFLINE=1.'); return 1; }
  let installed;
  try { installed = readVersion(); } catch { err('Cannot read installed package version.'); return 1; }
  if (typeof installed !== 'string' || !STABLE.test(installed)) { err('Installed version is not a stable X.Y.Z version.'); return 1; }
  const result = call(['view', PACKAGE, 'version']);
  if (result.error || result.status !== 0 || typeof result.stdout !== 'string') { err('Unable to query npm for the latest version. Check your network and npm configuration.'); return 1; }
  const latest = result.stdout.trim();
  if (!STABLE.test(latest)) { err('npm returned an invalid stable X.Y.Z version.'); return 1; }
  const available = compare(latest, installed) > 0;
  out(`Installed version: ${installed}`); out(`Latest published version: ${latest}`);
  out(available ? 'Update available.' : 'No update available.');
  if (args.includes('--check')) return available ? 10 : 0;
  if (!available) {
    reportPinnedHosts(installed, env, out);
    return 0;
  }
  const root = call(['root', '-g']);
  let kind;
  try { kind = installation(packageDir, root.status === 0 && typeof root.stdout === 'string' ? root.stdout.trim() : ''); } catch { kind = 'local'; }
  if (kind !== 'global') {
    err(kind === 'npx' ? `This is an npx cache installation. Run: npx -y ${PACKAGE}@latest ...` : `This is not a global npm installation. Run: ${MANUAL}`);
    return 1;
  }
  const command = `npm install -g ${PACKAGE}@${latest}`;
  out(`Will run: ${command}`);
  if (!args.includes('--yes')) {
    if (!tty) { err('Confirmation requires a TTY; use --yes to update non-interactively.'); return 1; }
    let answer;
    try { answer = await prompt(); } catch { err('Confirmation could not be read.'); return 1; }
    if (!/^y(?:es)?$/i.test(answer.trim())) { out('Update cancelled.'); return 1; }
  }
  const install = call(['install', '-g', `${PACKAGE}@${latest}`]);
  if (install.error || install.status !== 0) { err(`npm update failed. Manual command: ${MANUAL}`); return 1; }
  let after;
  try { after = readVersion(); } catch { /* report below */ }
  if (after !== latest) { err(`Installed version could not be verified after update. Manual command: ${MANUAL}`); return 1; }
  out(`Updated to ${latest}.`);
  reportPinnedHosts(latest, env, out);
  return 0;
}
module.exports = { update, compare, installation, npm };
