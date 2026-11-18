#!/usr/bin/env node
'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const packageManifest = require('../package.json');

const TARGETS = Object.freeze({
  'linux/x64': Object.freeze({ packageName: '@synapsync/syntra-linux-x64', executable: 'bin/syntra' }),
  'linux/arm64': Object.freeze({ packageName: '@synapsync/syntra-linux-arm64', executable: 'bin/syntra' }),
  'darwin/x64': Object.freeze({ packageName: '@synapsync/syntra-darwin-x64', executable: 'bin/syntra' }),
  'darwin/arm64': Object.freeze({ packageName: '@synapsync/syntra-darwin-arm64', executable: 'bin/syntra' }),
  'win32/x64': Object.freeze({ packageName: '@synapsync/syntra-win32-x64', executable: 'bin/syntra.exe' }),
});

const SUPPORTED_TARGETS = Object.keys(TARGETS).join(', ');
const SIGNALS = Object.freeze({
  win32: Object.freeze(['SIGINT', 'SIGBREAK', 'SIGTERM']),
  default: Object.freeze(['SIGHUP', 'SIGINT', 'SIGTERM']),
});
const SIGNAL_EXIT_CODES = Object.freeze({
  SIGHUP: 129,
  SIGINT: 130,
  SIGBREAK: 149,
  SIGTERM: 143,
});

function targetFor(platform, architecture) {
  const target = TARGETS[`${platform}/${architecture}`];
  if (!target) {
    throw new Error(
      `Syntra does not provide a binary for ${platform}/${architecture}. ` +
      `Supported targets: ${SUPPORTED_TARGETS}. Ask your administrator for a supported build.`,
    );
  }
  return target;
}

function resolveBinary(platform = process.platform, architecture = process.arch, resolve = require.resolve) {
  const target = targetFor(platform, architecture);
  const missingBinary = () => new Error(
    `The Syntra binary for ${platform}/${architecture} is missing ` +
    `(${target.packageName}). Reinstall @synapsync/syntra-mcp with optional dependencies enabled ` +
    `(npm install --include=optional) and make sure the matching platform package is available.`,
  );
  let installedManifest;
  let manifestPath;
  try {
    manifestPath = resolve(`${target.packageName}/package.json`);
    installedManifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  } catch {
    throw missingBinary();
  }
  const expectedVersion = packageManifest.optionalDependencies?.[target.packageName];
  if (!installedManifest || typeof installedManifest !== 'object' || Array.isArray(installedManifest) ||
      installedManifest.name !== target.packageName || expectedVersion !== packageManifest.version ||
      installedManifest.version !== expectedVersion) {
    throw new Error(
      `The installed ${target.packageName} package does not match launcher version ${packageManifest.version}. ` +
      `Reinstall @synapsync/syntra-mcp with its exact optional dependencies.`,
    );
  }
  const executablePath = path.join(path.dirname(manifestPath), target.executable);
  try {
    if (!fs.lstatSync(executablePath).isFile()) throw new Error('not a regular file');
    fs.accessSync(executablePath, platform === 'win32' ? fs.constants.F_OK : fs.constants.X_OK);
    return executablePath;
  } catch {
    throw missingBinary();
  }
}

function signalNames(platform = process.platform) {
  return platform === 'win32' ? SIGNALS.win32 : SIGNALS.default;
}

function signalExitCode(signal) {
  return SIGNAL_EXIT_CODES[signal] || 1;
}

function launch(args = process.argv.slice(2)) {
  if (args.length === 1 && (args[0] === '--version' || args[0] === 'version')) {
    process.stdout.write(`${packageManifest.version}\n`);
    return;
  }
  if (args[0] === 'update') {
    require('./update.cjs').update(args.slice(1)).then(code => { process.exitCode = code; }, () => {
      process.stderr.write('syntra: update failed.\n'); process.exitCode = 1;
    });
    return;
  }
  if (args[0] === 'setup') {
    const setup = require('./setup.cjs');
    setup.setupCLI(args.slice(1)).catch(error => {
      const message = error instanceof setup.SetupError ? error.message : 'Setup could not be completed safely.';
      process.stderr.write(`syntra: ${message}\n`);
      process.exitCode = 1;
    });
    return;
  }

  let executable;
  try {
    executable = resolveBinary();
  } catch (error) {
    process.stderr.write(`syntra: ${error.message}\n`);
    process.exitCode = 1;
    return;
  }

  let child;
  try {
    child = spawn(executable, args, {
      shell: false,
      stdio: 'inherit',
      windowsHide: true,
    });
  } catch {
    process.stderr.write(`syntra: unable to start the ${process.platform}/${process.arch} executable. Reinstall the matching platform package.\n`);
    process.exitCode = 1;
    return;
  }

  let receivedSignal = null;
  let handlersRemoved = false;
  const handlers = new Map();
  const removeHandlers = () => {
    if (handlersRemoved) return;
    handlersRemoved = true;
    for (const [signal, handler] of handlers) process.removeListener(signal, handler);
  };

  for (const signal of signalNames()) {
    const handler = () => {
      receivedSignal = signal;
      if (child.exitCode !== null || child.signalCode !== null) return;
      try {
        child.kill(signal);
      } catch {
        // The child may have exited between the state check and kill().
      }
    };
    handlers.set(signal, handler);
    process.on(signal, handler);
  }

  child.once('error', () => {
    removeHandlers();
    process.stderr.write(`syntra: unable to start the ${process.platform}/${process.arch} executable. Reinstall the matching platform package.\n`);
    process.exitCode = 1;
  });

  child.once('exit', (code, childSignal) => {
    removeHandlers();
    const signal = receivedSignal || childSignal;
    if (signal) {
      if (process.platform === 'win32') {
        process.exitCode = signalExitCode(signal);
      } else {
        try {
          process.kill(process.pid, signal);
        } catch {
          process.exitCode = signalExitCode(signal);
        }
      }
      return;
    }
    if (code === 0 && args.length === 1 && (args[0] === '--help' || args[0] === '-h' || args[0] === 'help')) {
      process.stdout.write('\nLauncher commands:\n  setup       Configure an agent host.\n  update      Update the installed package.\n  --version   Print the package version.\n');
    }
    process.exitCode = Number.isInteger(code) ? code : 1;
  });
}

if (require.main === module) launch();

module.exports = { targetFor, resolveBinary, signalNames, signalExitCode };
