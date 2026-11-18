#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ARTIFACTS = Object.freeze({
  'synapsync-syntra-0.0.0-handoff.tgz': 'e1d6bb4e3b3fc680dbac884385b475222a0a61e6ac9b1708f22ae1748c3c4639',
  'synapsync-syntra-darwin-arm64-0.0.0-handoff.tgz': '0591638e95b7a2720d34e97d9c00b8355beb9ce239ca1137c7fa98ff03e1ef71',
  'synapsync-syntra-darwin-x64-0.0.0-handoff.tgz': '196ff2b4f5cffc5e65c7ab248425096f5c436c9a5d200dc5e293d2f3513f9afa',
  'synapsync-syntra-linux-arm64-0.0.0-handoff.tgz': 'edd5841e3520b2008685059fdb15f6784de9ede194285155f417015acbf51199',
  'synapsync-syntra-linux-x64-0.0.0-handoff.tgz': 'b08d551382c6d0ca9a2ce57969eab2601b2ed20770f17f819d85c6a452502857',
  'synapsync-syntra-win32-x64-0.0.0-handoff.tgz': 'b4b1e5a0f9053099715d741090c0dd600171ae03a571c51e31e5c3f46f41daca',
});
const TARGETS = Object.freeze({
  'darwin/arm64': Object.freeze({
    artifact: 'synapsync-syntra-darwin-arm64-0.0.0-handoff.tgz',
    packageName: '@synapsync/syntra-darwin-arm64',
    binary: 'bin/syntra',
    binarySHA256: '8890c3e89337d829a34187d4705bff0934cd546659bc025f6865af92510512b9',
  }),
  'linux/arm64': Object.freeze({
    artifact: 'synapsync-syntra-linux-arm64-0.0.0-handoff.tgz',
    packageName: '@synapsync/syntra-linux-arm64',
    binary: 'bin/syntra',
    binarySHA256: 'cd48398d28086823c9309f9b735c7f11ce0c9cd46735d2b7cd5fc7b7059654dc',
  }),
});
const EXPECTED_TOOLS = Object.freeze([
  'identity', 'workspaces', 'projects', 'teams', 'members', 'resolve',
  'tickets', 'ticket', 'messages', 'snapshot', 'prepare', 'link', 'publish', 'retry',
]);
const EXPECTED_EMPTY_PROFILE_ERROR = 'syntra: no local connection; run human interactive login\n';

function fail(message) {
  throw new Error(message);
}

function parseArgs(args) {
  if (args.length !== 2 || args[0] !== '--artifacts') {
    fail('usage: node test-npm-artifacts.cjs --artifacts /absolute/path/to/tarballs');
  }
  if (!path.isAbsolute(args[1])) fail('artifact directory must be absolute');
  const directory = path.resolve(args[1]);
  return directory;
}

function sha256(file) {
  return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
}

function validateArtifactInventory(directory) {
  const info = fs.lstatSync(directory);
  assert.ok(info.isDirectory() && !info.isSymbolicLink(), 'artifact path must be a real directory');
  const actual = fs.readdirSync(directory).sort();
  const expected = Object.keys(ARTIFACTS).sort();
  assert.deepEqual(actual, expected, 'artifact directory must contain exactly the six selected T3.2 tarballs');
  for (const [name, digest] of Object.entries(ARTIFACTS)) {
    const file = path.join(directory, name);
    const stat = fs.lstatSync(file);
    assert.ok(stat.isFile() && !stat.isSymbolicLink(), `${name} must be a regular file`);
    assert.equal(sha256(file), digest, `${name} SHA-256 mismatch`);
  }
}

function findExecutable(name) {
  const suffix = process.platform === 'win32' ? '.cmd' : '';
  for (const directory of (process.env.PATH || '').split(path.delimiter)) {
    if (!directory) continue;
    const candidate = path.join(directory, `${name}${suffix}`);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return path.resolve(candidate);
    } catch {}
  }
  fail(`required executable not found on PATH: ${name}`);
}

function isolatedEnvironment(root, npmPath) {
  const home = path.join(root, 'home');
  const cache = path.join(root, 'npm-cache');
  const temp = path.join(root, 'tmp');
  const configs = path.join(root, 'config');
  const tools = path.join(root, 'tools');
  for (const directory of [home, cache, temp, configs, tools]) fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const windows = process.platform === 'win32';
  if (!windows) {
    fs.symlinkSync(process.execPath, path.join(tools, 'node'));
    fs.symlinkSync(npmPath, path.join(tools, 'npm'));
    fs.symlinkSync('/bin/sh', path.join(tools, 'sh'));
  }
  const userConfig = path.join(configs, 'user.npmrc');
  const globalConfig = path.join(configs, 'global.npmrc');
  fs.writeFileSync(userConfig, '', { mode: 0o600 });
  fs.writeFileSync(globalConfig, '', { mode: 0o600 });

  return {
    ...(windows ? { SystemRoot: process.env.SystemRoot, ComSpec: process.env.ComSpec, PATHEXT: process.env.PATHEXT } : {}),
    PATH: windows ? path.dirname(process.execPath) : tools,
    HOME: home,
    USERPROFILE: home,
    TMPDIR: temp,
    TEMP: temp,
    TMP: temp,
    npm_config_cache: cache,
    npm_config_userconfig: userConfig,
    npm_config_globalconfig: globalConfig,
    npm_config_offline: 'true',
    npm_config_ignore_scripts: 'true',
    npm_config_audit: 'false',
    npm_config_fund: 'false',
    npm_config_update_notifier: 'false',
    npm_config_yes: 'false',
    npm_config_registry: 'http://127.0.0.1:9',
  };
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env,
    input: options.input,
    encoding: 'utf8',
    timeout: options.timeout || 30000,
    maxBuffer: 2 * 1024 * 1024,
    windowsHide: true,
  });
  if (result.error) fail(`${path.basename(command)} failed to run: ${result.error.message}`);
  return { status: result.status, signal: result.signal, stdout: result.stdout || '', stderr: result.stderr || '' };
}

function checkEmptyStatus(result, label) {
  assert.equal(result.signal, null, `${label} was terminated by signal ${result.signal}`);
  assert.equal(result.status, 1, `${label} did not propagate the CLI's expected exit code 1; stderr=${result.stderr}`);
  assert.equal(result.stdout, '', `${label} wrote unexpected stdout: ${result.stdout}`);
  assert.equal(result.stderr, EXPECTED_EMPTY_PROFILE_ERROR, `${label} did not preserve the CLI's expected stderr`);
}

function parseFrames(stdout) {
  const lines = stdout.split(/\r?\n/).filter(line => line.length > 0);
  return lines.map((line, index) => {
    try {
      return JSON.parse(line);
    } catch {
      fail(`MCP stdout line ${index + 1} is not one complete JSON-RPC frame`);
    }
  });
}

function runMCP(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ['pipe', 'pipe', 'pipe'],
      shell: false,
      windowsHide: true,
    });
    let stdout = '';
    let stderr = '';
    let pendingLine = '';
    let closed = false;
    let closeResult;
    let deliveredFrames = 0;
    const frames = [];
    const waiters = [];
    const settleLine = line => {
      if (!line) return;
      try {
        frames.push(JSON.parse(line));
      } catch {
        child.kill('SIGKILL');
        reject(new Error('MCP stdout contains a line that is not one complete JSON-RPC frame'));
        return;
      }
      const waiter = waiters.shift();
      if (waiter) {
        deliveredFrames++;
        waiter.resolve(frames[frames.length - 1]);
      }
    };
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', chunk => {
      stdout += chunk;
      pendingLine += chunk;
      for (;;) {
        const newline = pendingLine.indexOf('\n');
        if (newline < 0) break;
        settleLine(pendingLine.slice(0, newline).replace(/\r$/, ''));
        pendingLine = pendingLine.slice(newline + 1);
      }
    });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.once('error', error => reject(new Error(`unable to start installed MCP command: ${error.message}`)));
    child.once('close', (code, signal) => {
      if (pendingLine) settleLine(pendingLine);
      closed = true;
      closeResult = { code, signal };
      for (const waiter of waiters.splice(0)) waiter.reject(new Error(`MCP exited before its response (code=${code}, signal=${signal})`));
    });

    function nextFrame() {
      if (frames.length > 0 && frames.length > deliveredFrames) {
        deliveredFrames++;
        return Promise.resolve(frames[deliveredFrames - 1]);
      }
      if (closed) return Promise.reject(new Error(`MCP exited before its response (code=${closeResult.code}, signal=${closeResult.signal})`));
      return new Promise((resolveLine, rejectLine) => {
        const timer = setTimeout(() => {
          const index = waiters.findIndex(waiter => waiter.resolve === wrappedResolve);
          if (index >= 0) waiters.splice(index, 1);
          rejectLine(new Error('timed out waiting for an MCP JSON-RPC response'));
        }, 8000);
        const wrappedResolve = value => { clearTimeout(timer); resolveLine(value); };
        const wrappedReject = error => { clearTimeout(timer); rejectLine(error); };
        waiters.push({ resolve: wrappedResolve, reject: wrappedReject });
      });
    }

    async function write(message) {
      await new Promise((done, failWrite) => {
        child.stdin.write(`${JSON.stringify(message)}\n`, error => error ? failWrite(error) : done());
      });
    }
    async function finish() {
      if (closed) return closeResult;
      return new Promise((done, failClose) => {
        const timer = setTimeout(() => {
          child.kill('SIGKILL');
          failClose(new Error('MCP did not exit after stdin EOF'));
        }, 8000);
        child.once('close', (code, signal) => {
          clearTimeout(timer);
          done({ code, signal });
        });
        child.stdin.end();
      });
    }
    async function exercise() {
      await write({ jsonrpc: '2.0', id: 1, method: 'initialize', params: {
        protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'tarball-smoke', version: '1' },
      } });
      const initialize = await nextFrame();
      await write({ jsonrpc: '2.0', method: 'notifications/initialized' });
      await write({ jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} });
      const list = await nextFrame();
      await write({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'identity', arguments: {} } });
      const identity = await nextFrame();
      const exit = await finish();
      resolve({ status: exit.code, signal: exit.signal, stdout, stderr, frames: [initialize, list, identity] });
    }
    exercise().catch(error => {
      if (!closed) child.kill('SIGKILL');
      reject(error);
    });
  });
}

function checkMCP(result) {
  assert.equal(result.signal, null, `MCP was terminated by signal ${result.signal}`);
  assert.equal(result.status, 0, `MCP did not exit successfully after stdin EOF; stderr=${result.stderr}`);
  assert.equal(result.stderr, '', `MCP wrote unexpected stderr: ${result.stderr}`);
  const frames = result.frames || parseFrames(result.stdout);
  assert.equal(result.stdout.split(/\r?\n/).filter(line => line.length > 0).length, frames.length,
    'MCP stdout must contain only the tested JSON-RPC responses');
  assert.equal(frames.length, 3, 'MCP stdout must contain exactly initialize, tools/list and tools/call responses');
  for (let index = 0; index < frames.length; index++) {
    assert.equal(frames[index].jsonrpc, '2.0', `MCP response ${index + 1} is missing JSON-RPC version`);
    assert.equal(frames[index].id, index + 1, `MCP response ${index + 1} has the wrong request ID`);
    assert.equal(frames[index].error, undefined, `MCP request ${index + 1} returned a protocol error`);
  }
  assert.ok(frames[0].result?.serverInfo?.name === 'syntra', 'MCP initialize did not identify the packaged server');
  const tools = frames[1].result?.tools;
  assert.ok(Array.isArray(tools), 'MCP tools/list did not return a tool catalog');
  assert.deepEqual(tools.map(tool => tool.name).sort(), [...EXPECTED_TOOLS].sort(), 'MCP allowlist differs from the fourteen-tool contract');
  assert.equal(frames[2].result?.isError, true, 'identity call without a profile must return a safe tool error');
  const text = (frames[2].result?.content || []).map(item => item.text || '').join('\n');
  assert.match(text, /local connection unavailable or unauthorized/);
}

async function main() {
  const artifactDirectory = parseArgs(process.argv.slice(2));
  const targetKey = `${process.platform}/${process.arch}`;
  const target = TARGETS[targetKey];
  if (!target) fail(`smoke target ${targetKey} is unsupported here; validated targets are darwin/arm64 and linux/arm64`);
  if (Number(process.versions.node.split('.')[0]) < 22 ||
      (Number(process.versions.node.split('.')[0]) === 22 && Number(process.versions.node.split('.')[1]) < 12)) {
    fail(`Node >=22.12 is required, got ${process.version}`);
  }
  validateArtifactInventory(artifactDirectory);

  const npmPath = findExecutable('npm');
  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'syntra npm smoke '));
  try {
    fs.chmodSync(scratch, 0o700);
    const env = isolatedEnvironment(scratch, npmPath);
    const npmVersion = run(npmPath, ['--version'], { cwd: scratch, env });
    assert.equal(npmVersion.status, 0, `npm version check failed: ${npmVersion.stderr}`);
    const root = path.join(scratch, 'consumer');
    const tarballs = path.join(root, 'tarballs');
    fs.mkdirSync(tarballs, { recursive: true, mode: 0o700 });
    const rootTarball = 'synapsync-syntra-0.0.0-handoff.tgz';
    for (const name of [rootTarball, target.artifact]) {
      fs.copyFileSync(path.join(artifactDirectory, name), path.join(tarballs, name), fs.constants.COPYFILE_EXCL);
    }
    fs.writeFileSync(path.join(root, 'package.json'), `${JSON.stringify({
      name: 'syntra-tarball-smoke-consumer',
      version: '1.0.0',
      private: true,
      dependencies: {
        '@synapsync/syntra': `file:tarballs/${rootTarball}`,
        [target.packageName]: `file:tarballs/${target.artifact}`,
      },
    }, null, 2)}\n`, { mode: 0o600 });

    const install = run(npmPath, [
      'install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund',
      '--registry=http://127.0.0.1:9', '--cache', env.npm_config_cache,
    ], { cwd: root, env, timeout: 60000 });
    assert.equal(install.signal, null, `npm install was terminated by ${install.signal}`);
    assert.equal(install.status, 0, `offline tarball install failed; stdout=${install.stdout}\nstderr=${install.stderr}`);

    const installedRoot = path.join(root, 'node_modules', '@synapsync', 'syntra');
    const installedPlatform = path.join(root, 'node_modules', ...target.packageName.split('/'));
    assert.deepEqual(
      fs.readdirSync(path.join(root, 'node_modules', '@synapsync')).sort(),
      ['syntra', target.packageName.split('/')[1]].sort(),
      'clean install must contain only the root package and matching platform package',
    );
    const rootManifest = JSON.parse(fs.readFileSync(path.join(installedRoot, 'package.json'), 'utf8'));
    const platformManifest = JSON.parse(fs.readFileSync(path.join(installedPlatform, 'package.json'), 'utf8'));
    assert.equal(rootManifest.name, '@synapsync/syntra');
    assert.equal(rootManifest.version, '0.0.0-handoff');
    assert.equal(platformManifest.name, target.packageName);
    assert.equal(platformManifest.version, rootManifest.version);
    const installedBinary = path.join(installedPlatform, ...target.binary.split('/'));
    assert.equal(sha256(installedBinary), target.binarySHA256, 'installed executable differs from the T3.2 build digest');
    const command = path.join(root, 'node_modules', '.bin', 'syntra');
    assert.ok(fs.existsSync(command), 'npm did not create the installed syntra command');
    assert.equal(fs.realpathSync(command), fs.realpathSync(path.join(installedRoot, 'bin', 'launcher.cjs')),
      'installed command does not resolve to the packed launcher');

    const profileDirectory = path.join(scratch, 'isolated profile with spaces');
    fs.mkdirSync(profileDirectory, { mode: 0o700 });
    const statusArgs = ['status', '--profile-dir', profileDirectory];
    checkEmptyStatus(run(command, statusArgs, { cwd: root, env }), 'installed command');
    checkEmptyStatus(run(npmPath, [
      'exec', '--offline', '--yes=false', '--silent', '--', 'syntra', ...statusArgs,
    ], { cwd: root, env }), 'npm exec');

    const mcp = await runMCP(command, ['mcp', '--profile-dir', profileDirectory], { cwd: root, env });
    checkMCP(mcp);
    assert.deepEqual(fs.readdirSync(profileDirectory).sort(), ['.lock'], 'smoke must not synthesize or persist a connection profile');

    console.log(`PASS target=${targetKey} Node=${process.version} npm=${npmVersion.stdout.trim()}`);
    console.log(`PASS root=${sha256(path.join(artifactDirectory, rootTarball))}`);
    console.log(`PASS platform=${target.packageName} tarball=${ARTIFACTS[target.artifact]} binary=${target.binarySHA256}`);
    console.log('PASS offline install, direct command and npm exec status stderr/exit, MCP initialize/tools/list/identity-error/EOF');
    console.log('PASS profile isolated; no credentials, API call, source checkout, Go or Git used');
  } finally {
    fs.rmSync(scratch, { recursive: true, force: true });
  }
}

if (require.main === module) {
  try {
    main().catch(error => {
      console.error(`FAIL npm artifact smoke: ${error.message}`);
      process.exitCode = 1;
    });
  } catch (error) {
    console.error(`FAIL npm artifact smoke: ${error.message}`);
    process.exitCode = 1;
  }
}

module.exports = { sha256, findExecutable, isolatedEnvironment, run, runMCP, checkMCP };
