'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const PACKAGE = '@synapsync/syntra-mcp';
const STABLE = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;

function realPath(file) {
  try { return fs.realpathSync(file); } catch { return null; }
}

function samePath(left, right) {
  return process.platform === 'win32' ? left.toLowerCase() === right.toLowerCase() : left === right;
}

function packagePrefix(packageDir) {
  const real = realPath(packageDir);
  if (!real || real.split(path.sep).some(part => part.toLowerCase() === '_npx')) return null;
  const modules = path.dirname(path.dirname(real));
  const parent = path.dirname(modules);
  const prefix = process.platform === 'win32' ? parent : path.dirname(parent);
  const expected = path.join(prefix, ...(process.platform === 'win32' ? [] : ['lib']), 'node_modules', PACKAGE);
  return samePath(real, expected) ? prefix : null;
}

function npmCliCandidates(prefix = null, env = process.env) {
  const dirs = [prefix && path.join(prefix, process.platform === 'win32' ? '' : 'bin'), path.dirname(process.execPath)];
  return [
    ...dirs.filter(Boolean).flatMap(dir => [
      path.join(dir, 'node_modules', 'npm', 'bin', 'npm-cli.js'),
      path.resolve(dir, '../lib/node_modules/npm/bin/npm-cli.js'),
      path.join(dir, 'npm'),
    ]),
    ...(env.PATH || env.Path || '').split(path.delimiter).filter(path.isAbsolute).flatMap(dir => [
      path.join(dir, 'node_modules', 'npm', 'bin', 'npm-cli.js'),
      path.join(dir, 'npm'),
    ]),
  ].map(realPath).filter(file => file && path.basename(file) === 'npm-cli.js');
}

function runNpm(command, initial, env) {
  return args => spawnSync(command, [...initial, ...args], {
    shell: false, env, encoding: 'utf8', timeout: 30000, maxBuffer: 1024 * 1024, windowsHide: true,
  });
}

function npmCall(env, prefix) {
  const cli = npmCliCandidates(prefix, env)[0];
  if (cli) return runNpm(process.execPath, [cli], env);
  return process.platform === 'win32'
    ? () => ({ status: null })
    : runNpm('npm', [], env);
}

function verifyGlobal(packageDir, prefix, call) {
  if (!prefix) return false;
  const root = call(['root', '-g', '--prefix', prefix]);
  if (root.error || root.status !== 0 || typeof root.stdout !== 'string' || !path.isAbsolute(root.stdout.trim())) return false;
  const expected = realPath(path.join(root.stdout.trim(), PACKAGE));
  const actual = realPath(packageDir);
  return Boolean(expected && actual && samePath(expected, actual));
}

module.exports = { PACKAGE, STABLE, npmCall, npmCliCandidates, packagePrefix, verifyGlobal, realPath, samePath };
