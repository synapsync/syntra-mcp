'use strict';

const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline/promises');

const { PACKAGE, STABLE, npmCall, packagePrefix, verifyGlobal, realPath } = require('./npm-installation.cjs');
const MANUAL = `npm install -g ${PACKAGE}@latest`;

function compare(a, b) {
  const left = a.split('.').map(BigInt);
  const right = b.split('.').map(BigInt);
  for (let i = 0; i < 3; i++) if (left[i] !== right[i]) return left[i] > right[i] ? 1 : -1;
  return 0;
}

function reportHosts(env, packageDir, out) {
  const { AGENT_NAMES, registrationState } = require('./setup.cjs');
  out('User-level Syntra registrations in this environment:');
  for (const agent of AGENT_NAMES) {
    let state;
    try { state = registrationState(agent, env, process.platform, packageDir); } catch {
      state = { mode: 'unresolved' };
    }
    let description;
    switch (state.mode) {
      case 'absent': description = 'absent in inspected config'; break;
      case 'unresolved': description = 'unresolved config or custom launcher'; break;
      case 'pinned': description = STABLE.test(state.pin) ? `pinned to ${state.pin}` : 'custom npm pin or tag'; break;
      case 'latest': description = 'latest (npm resolves at process startup)'; break;
      case 'local':
        description = state.target === 'current' ? 'local (this installation)' : state.target === 'other'
          ? 'local (a different path)' : 'local (unresolved executable or package path)';
        break;
      case 'direct': description = 'direct executable (PATH resolution unverified)'; break;
      default: description = 'unversioned npm registration';
    }
    const runtime = ['absent', 'unresolved'].includes(state.mode) ? '' : '; active process not verified';
    out(`  ${agent}: ${description}${runtime}.`);
  }
}

async function update(args, deps = {}) {
  const out = deps.out || (message => process.stdout.write(message + '\n'));
  const err = deps.err || (message => process.stderr.write(`syntra: ${message}\n`));
  const packageDir = deps.packageDir || path.resolve(__dirname, '..');
  const env = deps.env || process.env;
  const prefix = packagePrefix(packageDir);
  const call = deps.npm || npmCall(env, prefix);
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
  const result = call(['view', `${PACKAGE}@latest`, 'version', '--prefer-online']);
  if (result.error || result.status !== 0 || typeof result.stdout !== 'string') { err('Unable to query npm for the latest version. Check your network and npm configuration.'); return 1; }
  const latest = result.stdout.trim();
  if (!STABLE.test(latest)) { err('npm returned an invalid stable X.Y.Z version.'); return 1; }
  const available = compare(latest, installed) > 0;
  out(`Installed version: ${installed}`); out(`Latest published version: ${latest}`);
  out(available ? 'Update available.' : 'No update available.');
  reportHosts(env, packageDir, out);
  if (args.includes('--check')) return available ? 10 : 0;
  if (!available) return 0;
  if (!verifyGlobal(packageDir, prefix, call)) {
    const isNpx = realPath(packageDir)?.split(path.sep).includes('_npx');
    err(isNpx ? `This is an npx cache installation. Run: npx -y ${PACKAGE}@latest ...`
      : `The executing package's global npm prefix could not be verified. Run: ${MANUAL}`);
    return 1;
  }
  out(`Will install ${PACKAGE}@${latest} globally in npm prefix ${JSON.stringify(prefix)}.`);
  if (!args.includes('--yes')) {
    if (!tty) { err('Confirmation requires a TTY; use --yes to update non-interactively.'); return 1; }
    let answer;
    try { answer = await prompt(); } catch { err('Confirmation could not be read.'); return 1; }
    if (!/^y(?:es)?$/i.test(answer.trim())) { out('Update cancelled.'); return 1; }
  }
  let current;
  try { current = readVersion(); } catch { /* Report the changed installation below. */ }
  if (current !== installed || !verifyGlobal(packageDir, prefix, call)) {
    err('The global npm installation changed during confirmation. Run update again.');
    return 1;
  }
  const install = call(['install', '-g', '--prefix', prefix, `${PACKAGE}@${latest}`]);
  if (install.error || install.status !== 0) { err(`npm update failed. Manual command: ${MANUAL}`); return 1; }
  let after;
  try { after = readVersion(); } catch { /* report below */ }
  if (after !== latest) { err(`Installed version could not be verified after update. Manual command: ${MANUAL}`); return 1; }
  out(`Updated to ${latest}.`);
  return 0;
}
module.exports = { update, compare };
