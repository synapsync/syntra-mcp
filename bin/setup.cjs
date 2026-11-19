'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { randomUUID } = require('node:crypto');
const { isDeepStrictEqual, TextDecoder } = require('node:util');
const jsonc = require('./vendor/jsonc-parser/lib/umd/main.js');
const toml = require('./vendor/smol-toml/index.cjs');
const readline = require('node:readline/promises');
const { PACKAGE, STABLE, npmCall, npmCliCandidates, packagePrefix, verifyGlobal, realPath, samePath } = require('./npm-installation.cjs');

const MAX_CONFIG_BYTES = 4 * 1024 * 1024;
const SKILL_FILES = Object.freeze(['SKILL.md', 'references/tools.md']);
const SKILL_DIRECTORIES = Object.freeze(['references']);
const AGENT_NAMES = Object.freeze(['codex', 'claude-code', 'opencode']);

class SetupError extends Error {
  constructor(message) {
    super(message);
    this.name = 'SetupError';
  }
}

function fail(message) {
  throw new SetupError(message);
}

function parseSetupArgs(args) {
  let agent = null;
  let dryRun = false;
  let sawAgent = false;
  let sawDryRun = false;
  let replace = false;
  let mode = null;

  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === '--agent') {
      if (sawAgent) fail('--agent may be supplied only once.');
      sawAgent = true;
      const value = args[index + 1];
      if (!value || value.startsWith('--')) fail('--agent requires codex, claude-code, or opencode.');
      agent = value;
      index += 1;
    } else if (argument === '--dry-run') {
      if (sawDryRun) fail('--dry-run may be supplied only once.');
      sawDryRun = true;
      dryRun = true;
    } else if (argument === '--mode') {
      if (mode) fail('--mode may be supplied only once.');
      mode = args[++index];
      if (!['latest', 'local'].includes(mode)) fail('--mode requires latest or local.');
    } else if (argument === '--replace') {
      if (replace) fail('--replace may be supplied only once.');
      replace = true;
    } else {
      fail('Unknown setup option. Use --agent, --mode, --dry-run, and --replace only.');
    }
  }

  if (!agent) fail('Choose a host with --agent codex, --agent claude-code, or --agent opencode.');
  if (!AGENT_NAMES.includes(agent)) fail('Supported setup hosts are codex, claude-code, and opencode.');
  if (replace && !mode) fail('--replace requires an explicit --mode latest or local.');
  return { agent, dryRun, replace, mode };
}

function pathApi(platform) {
  return platform === 'win32' ? path.win32 : path;
}

function homeDirectory(env, platform, pathModule) {
  const value = platform === 'win32'
    ? (env.USERPROFILE || env.HOME)
    : (env.HOME || env.USERPROFILE);
  if (typeof value !== 'string' || !value || !pathModule.isAbsolute(value)) {
    fail('An absolute user home directory is required to preview setup.');
  }
  return pathModule.normalize(value);
}

function selectedDirectory(env, variable, fallback, pathModule, label) {
  const value = env[variable];
  if (value === undefined || value === '') return fallback;
  if (typeof value !== 'string' || !pathModule.isAbsolute(value)) {
    fail(`${variable} must be an absolute path for ${label}.`);
  }
  return pathModule.normalize(value);
}

function shellCommand(platform, mode = 'latest', options = {}) {
  const executable = options.node || process.execPath;
  if (mode === 'local') {
    const directory = options.packageDir || path.resolve(__dirname, '..');
    const prefix = packagePrefix(directory);
    const call = options.npm || npmCall(options.env || process.env, prefix);
    const launcher = path.join(directory, 'bin', 'launcher.cjs');
    if (!realPath(executable) || !realPath(launcher) || !verifyGlobal(directory, prefix, call)) {
      fail(`Local mode requires running setup from the selected npm-global installation. Run: npm install -g ${PACKAGE}@latest`);
    }
    return { command: executable, args: [launcher, 'mcp'] };
  }
  const args = ['--yes', '--prefer-online', `${PACKAGE}@latest`, 'mcp'];
  if (platform === 'win32') {
    const npmCli = npmCliCandidates(null, options.env)[0];
    const npxCli = options.npxCli || (npmCli && realPath(path.join(path.dirname(npmCli), 'npx-cli.js')));
    if (!npxCli) fail('Windows latest mode requires an npm installation with npx-cli.js available.');
    return { command: executable, args: [npxCli, ...args] };
  }
  return { command: 'npx', args };
}

function tomlValue(value) {
  if (Array.isArray(value)) return `[${value.map(item => JSON.stringify(item)).join(', ')}]`;
  return JSON.stringify(value);
}

function hostPlan(agent, env = process.env, platform = process.platform) {
  const paths = pathApi(platform);
  const home = homeDirectory(env, platform, paths);


  if (agent === 'codex') {
    const configHome = selectedDirectory(
      env,
      'CODEX_HOME',
      paths.join(home, '.codex'),
      paths,
      'Codex',
    );
    return {
      agent,
      displayName: 'Codex',
      format: 'toml',
      configPath: paths.join(configHome, 'config.toml'),
      skillPath: paths.join(home, '.agents', 'skills', 'syntra'),
      registrationPath: ['mcp_servers', 'syntra'],
    };
  }

  if (agent === 'claude-code') {
    const configDirectory = env.CLAUDE_CONFIG_DIR === undefined || env.CLAUDE_CONFIG_DIR === ''
      ? null
      : selectedDirectory(env, 'CLAUDE_CONFIG_DIR', null, paths, 'Claude Code');
    const claudeDirectory = configDirectory || paths.join(home, '.claude');
    return {
      agent,
      displayName: 'Claude Code',
      format: 'json',
      configPath: configDirectory
        ? paths.join(configDirectory, '.claude.json')
        : paths.join(home, '.claude.json'),
      skillPath: paths.join(claudeDirectory, 'skills', 'syntra'),
      registrationPath: ['mcpServers', 'syntra'],
    };
  }

  if (agent === 'opencode') {
    const customOverrides = ['OPENCODE_CONFIG', 'OPENCODE_CONFIG_DIR', 'OPENCODE_CONFIG_CONTENT']
      .filter(variable => env[variable] !== undefined && env[variable] !== '');
    if (customOverrides.length > 0) {
      fail(`OpenCode custom environment override${customOverrides.length === 1 ? '' : 's'} active (${customOverrides.join(', ')}); clear them before previewing global setup.`);
    }
    const xdgConfigHome = selectedDirectory(
      env,
      'XDG_CONFIG_HOME',
      paths.join(home, '.config'),
      paths,
      'OpenCode',
    );
    const configDirectory = paths.join(xdgConfigHome, 'opencode');
    const jsonPath = paths.join(configDirectory, 'opencode.json');
    const jsoncPath = paths.join(configDirectory, 'opencode.jsonc');
    const jsonInfo = inspectPath(jsonPath, 'file', platform);
    const jsoncInfo = inspectPath(jsoncPath, 'file', platform);
    if (jsonInfo.exists && jsoncInfo.exists) {
      fail('OpenCode has both global JSON and JSONC config files; choose one before setup.');
    }
    return {
      agent,
      displayName: 'OpenCode',
      format: 'jsonc',
      configPath: jsoncInfo.exists ? jsoncPath : jsonPath,
      skillPath: paths.join(xdgConfigHome, 'opencode', 'skills', 'syntra'),
      registrationPath: ['mcp', 'servers', 'syntra'],
      openCodeConfigCandidates: [jsonPath, jsoncPath],
    };
  }

  fail('Supported setup hosts are codex, claude-code, and opencode.');
}

function inspectPath(target, finalKind, platform = process.platform) {
  const paths = pathApi(platform);
  if (typeof target !== 'string' || !paths.isAbsolute(target)) {
    fail('A setup destination must be an absolute path.');
  }
  const normalized = paths.normalize(target);
  const root = paths.parse(normalized).root;
  const components = normalized.slice(root.length).split(paths.sep).filter(Boolean);
  let cursor = root;
  let missing = false;
  let finalStat = null;

  for (let index = 0; index < components.length; index += 1) {
    cursor = paths.join(cursor, components[index]);
    if (missing) continue;
    let info;
    try {
      info = fs.lstatSync(cursor);
    } catch (error) {
      if (error && error.code === 'ENOENT') {
        missing = true;
        continue;
      }
      fail('A setup destination path could not be safely inspected.');
    }

    if (info.isSymbolicLink()) fail('A setup destination contains a symbolic link.');
    const isFinal = index === components.length - 1;
    if (!isFinal && !info.isDirectory()) fail('A setup destination has a non-directory parent.');
    if (isFinal && finalKind === 'file' && !info.isFile()) fail('A setup config destination is not a regular file.');
    if (isFinal && finalKind === 'directory' && !info.isDirectory()) fail('A skill destination is not a directory.');
    if (isFinal && finalKind === 'file' && info.nlink > 1) fail('A setup config destination has multiple hard links.');
    if (isFinal) finalStat = info;
  }

  return { exists: !missing, stat: finalStat };
}

function fileObservation(stat) {
  return {
    dev: stat.dev,
    ino: stat.ino,
    mode: stat.mode & 0o777,
    nlink: stat.nlink,
    size: stat.size,
    mtimeMs: stat.mtimeMs,
    ctimeMs: stat.ctimeMs,
  };
}

function sameFileObservation(left, right) {
  return isDeepStrictEqual(fileObservation(left), fileObservation(right));
}

function sameFileIdentity(left, right) {
  return left.dev === right.dev && left.ino === right.ino;
}

function sameConfigSnapshot(expected, current) {
  if (!expected.exists || !current.exists) return expected.exists === current.exists;
  return isDeepStrictEqual(expected.identity, current.identity) && expected.bytes.equals(current.bytes);
}

function writeAll(fd, bytes) {
  let offset = 0;
  while (offset < bytes.length) {
    const written = fs.writeSync(fd, bytes, offset, bytes.length - offset);
    if (written <= 0) fail('A setup file could not be written completely.');
    offset += written;
  }
}

function ensureDirectoryChain(target, platform, createdDirectories) {
  const paths = pathApi(platform);
  const normalized = paths.normalize(target);
  if (!paths.isAbsolute(normalized)) fail('A setup directory must be absolute.');
  const root = paths.parse(normalized).root;
  const components = normalized.slice(root.length).split(paths.sep).filter(Boolean);
  let current = root;

  for (const component of components) {
    current = paths.join(current, component);
    let stat;
    try {
      stat = fs.lstatSync(current);
    } catch (error) {
      if (!error || error.code !== 'ENOENT') fail('A setup directory could not be safely inspected.');
      try {
        fs.mkdirSync(current, { mode: 0o700 });
        stat = fs.lstatSync(current);
        const record = { path: current, identity: fileObservation(stat) };
        createdDirectories.push(record);
        if (platform !== 'win32') fs.chmodSync(current, 0o700);
        stat = fs.lstatSync(current);
      } catch (createError) {
        if (createError && createError.code === 'EEXIST') {
          try { stat = fs.lstatSync(current); } catch { fail('A setup directory could not be safely inspected.'); }
        } else {
          fail('A setup directory could not be created safely.');
        }
      }
    }
    if (stat.isSymbolicLink()) fail('A setup destination contains a symbolic link.');
    if (!stat.isDirectory()) fail('A setup destination has a non-directory parent.');
  }
}

function removeCreatedDirectories(createdDirectories) {
  for (const created of [...createdDirectories].reverse()) {
    try {
      const stat = fs.lstatSync(created.path);
      if (!stat.isDirectory() || stat.isSymbolicLink() ||
          !sameFileIdentity(created.identity, fileObservation(stat))) continue;
      fs.rmdirSync(created.path);
    } catch (error) {
      if (!error || !['ENOENT', 'ENOTEMPTY', 'EEXIST'].includes(error.code)) {
        // A nonempty or replaced directory is intentionally left for its owner.
      }
    }
  }
}

function readConfig(configPath, format, platform) {
  const info = inspectPath(configPath, 'file', platform);
  if (!info.exists) return { exists: false, text: '', bytes: Buffer.alloc(0), snapshot: { exists: false } };
  if (!info.stat || info.stat.size > MAX_CONFIG_BYTES) fail('The host config is too large to inspect safely.');
  let fd;
  try {
    const noFollow = fs.constants.O_NOFOLLOW || 0;
    fd = fs.openSync(configPath, fs.constants.O_RDONLY | noFollow);
    const before = fs.fstatSync(fd);
    if (!before.isFile() || before.nlink > 1 || before.size > MAX_CONFIG_BYTES) {
      fail('The host config is not a safe regular file.');
    }
    const bytes = fs.readFileSync(fd);
    const after = fs.fstatSync(fd);
    const currentPath = fs.lstatSync(configPath);
    if (currentPath.isSymbolicLink() || !sameFileObservation(before, after) ||
        !sameFileObservation(after, currentPath)) {
      fail('The host config changed while it was being inspected.');
    }
    const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
    if (!Buffer.from(text, 'utf8').equals(bytes)) fail(`The ${format.toUpperCase()} host config is not lossless UTF-8.`);
    return {
      exists: true,
      text,
      bytes,
      snapshot: { exists: true, identity: fileObservation(after), bytes, mode: after.mode & 0o777 },
    };
  } catch (error) {
    if (error instanceof SetupError) throw error;
    if (error instanceof TypeError) fail(`The ${format.toUpperCase()} host config is not valid UTF-8.`);
    fail(`The ${format.toUpperCase()} host config cannot be read safely.`);
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch { /* The read result or safe error is already determined. */ }
    }
  }
}

function readBundledSkill() {
  const skillRoot = path.resolve(__dirname, '..', 'skills', 'syntra');
  const files = new Map();
  for (const relative of SKILL_FILES) {
    const source = path.join(skillRoot, ...relative.split('/'));
    const info = inspectPath(source, 'file', process.platform);
    if (!info.exists || !info.stat || info.stat.size > MAX_CONFIG_BYTES) {
      fail('The bundled Syntra skill is incomplete.');
    }
    try {
      files.set(relative, fs.readFileSync(source));
    } catch {
      fail('The bundled Syntra skill cannot be read.');
    }
  }
  return { skillRoot, files };
}

function inspectExistingSkill(skillPath, bundleFiles, platform) {
  const info = inspectPath(skillPath, 'directory', platform);
  if (!info.exists) return 'absent';

  const observedFiles = [];
  const observedDirectories = [];
  const paths = pathApi(platform);
  const walk = (current, relative) => {
    let entries;
    try {
      entries = fs.readdirSync(current, { withFileTypes: true });
    } catch {
      fail('The existing skill directory cannot be safely inspected.');
    }
    for (const entry of entries) {
      const child = paths.join(current, entry.name);
      const childRelative = relative ? `${relative}/${entry.name}` : entry.name;
      const childInfo = inspectPath(child, entry.isDirectory() ? 'directory' : 'file', platform);
      if (entry.isSymbolicLink() || !childInfo.exists) fail('The existing skill contains an unsafe path.');
      if (entry.isDirectory()) {
        observedDirectories.push(childRelative);
        walk(child, childRelative);
      } else if (entry.isFile()) {
        observedFiles.push(childRelative);
      } else {
        fail('The existing skill contains a non-regular file.');
      }
    }
  };
  walk(skillPath, '');

  observedFiles.sort();
  observedDirectories.sort();
  if (!isDeepStrictEqual(observedFiles, [...bundleFiles.keys()].sort()) ||
      !isDeepStrictEqual(observedDirectories, [...SKILL_DIRECTORIES].sort())) {
    return 'conflict';
  }

  for (const relative of bundleFiles.keys()) {
    try {
      const actual = fs.readFileSync(paths.join(skillPath, ...relative.split('/')));
      if (!actual.equals(bundleFiles.get(relative))) return 'conflict';
    } catch {
      return 'conflict';
    }
  }
  return 'matching';
}

function propertyChildren(node) {
  if (!node || node.type !== 'object' || !Array.isArray(node.children)) return [];
  return node.children.filter(child => child.type === 'property');
}

function assertUnambiguousJsonTree(node) {
  if (!node) return;
  if (node.type === 'object') {
    const names = new Set();
    for (const property of propertyChildren(node)) {
      const keyNode = property.children && property.children[0];
      if (!keyNode || typeof keyNode.value !== 'string') fail('The host config has an ambiguous object key.');
      if (names.has(keyNode.value)) fail('The host config contains duplicate object keys.');
      if (['__proto__', 'constructor', 'prototype'].includes(keyNode.value)) {
        fail('The host config contains an unsafe object key.');
      }
      names.add(keyNode.value);
      assertUnambiguousJsonTree(property.children[1]);
    }
  } else if (node.children) {
    for (const child of node.children) assertUnambiguousJsonTree(child);
  }
}

function parseJsonDocument(text, format, allowComments) {
  const errors = [];
  const tree = jsonc.parseTree(text, errors, {
    allowEmptyContent: false,
    allowTrailingComma: allowComments,
    disallowComments: !allowComments,
  });
  if (errors.length || !tree || tree.type !== 'object') {
    fail(`The ${format.toUpperCase()} host config is malformed or is not an object.`);
  }
  assertUnambiguousJsonTree(tree);
  return { tree, value: jsonc.getNodeValue(tree) };
}

function getOwnPath(value, segments) {
  let current = value;
  for (const segment of segments) {
    if (!current || typeof current !== 'object' || Array.isArray(current) ||
        !Object.prototype.hasOwnProperty.call(current, segment)) return { exists: false, value: undefined };
    current = current[segment];
  }
  return { exists: true, value: current };
}

function deepPlainCopy(value) {
  if (Array.isArray(value)) return value.map(deepPlainCopy);
  if (!value || typeof value !== 'object') return value;
  const copy = {};
  for (const [key, child] of Object.entries(value)) copy[key] = deepPlainCopy(child);
  return copy;
}

function removeRegistrationAndNewParents(value, segments, original) {
  const copy = deepPlainCopy(value);
  let current = copy;
  for (const segment of segments.slice(0, -1)) {
    if (!current || typeof current !== 'object' || !Object.prototype.hasOwnProperty.call(current, segment)) return copy;
    current = current[segment];
  }
  if (!current || typeof current !== 'object') return copy;
  delete current[segments[segments.length - 1]];

  for (let length = segments.length - 1; length > 0; length -= 1) {
    const prefix = segments.slice(0, length);
    const candidate = getOwnPath(copy, prefix);
    const wasPresent = getOwnPath(original, prefix).exists;
    if (!candidate.exists || wasPresent || !candidate.value || typeof candidate.value !== 'object' ||
        Array.isArray(candidate.value) || Object.keys(candidate.value).length !== 0) break;
    let parent = copy;
    for (const segment of prefix.slice(0, -1)) parent = parent[segment];
    delete parent[prefix[prefix.length - 1]];
  }
  return copy;
}

function checkJsonParentTypes(tree, registrationPath) {
  let current = tree;
  for (const segment of registrationPath.slice(0, -1)) {
    const next = jsonc.findNodeAtLocation(current, [segment]);
    if (!next) return;
    if (next.type !== 'object') fail('The host config has a conflicting parent value for the Syntra registration.');
    current = next;
  }
}

function jsonLineEnding(text) {
  const match = /\r\n|\n|\r/.exec(text);
  return match ? match[0] : '\n';
}

function indentationAt(text, offset) {
  const lineStart = Math.max(text.lastIndexOf('\n', offset - 1), text.lastIndexOf('\r', offset - 1)) + 1;
  const prefix = text.slice(lineStart, offset);
  return /^[\t ]*$/.test(prefix) ? prefix : '';
}

function lastCommentEnd(text, objectNode) {
  const closeOffset = objectNode.offset + objectNode.length - 1;
  const scanner = jsonc.createScanner(text, false);
  let last = -1;
  while (true) {
    const kind = scanner.scan();
    const tokenOffset = scanner.getTokenOffset();
    if (kind === jsonc.SyntaxKind.EOF || tokenOffset >= closeOffset) break;
    if (tokenOffset > objectNode.offset &&
        (kind === jsonc.SyntaxKind.LineCommentTrivia || kind === jsonc.SyntaxKind.BlockCommentTrivia)) {
      last = tokenOffset + scanner.getTokenLength();
    }
  }
  return last;
}

function valueForMissingPath(segments, value) {
  let nested = value;
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    const wrapper = Object.create(null);
    wrapper[segments[index]] = nested;
    nested = wrapper;
  }
  return nested;
}

function serializeJsonValue(value, inline, indent, eol) {
  if (inline) return JSON.stringify(value);
  return JSON.stringify(value, null, 2)
    .split('\n')
    .map((line, index) => index === 0 ? line : `${indent}${line}`)
    .join(eol);
}

function insertJsonProperty(text, objectNode, key, value) {
  const openOffset = objectNode.offset;
  const closeOffset = openOffset + objectNode.length - 1;
  const properties = propertyChildren(objectNode);
  const eol = jsonLineEnding(text.slice(openOffset, closeOffset + 1));
  const closeIndent = indentationAt(text, closeOffset);
  const nested = valueForMissingPath([key], value);

  if (properties.length > 0) {
    const first = properties[0];
    const leading = text.slice(openOffset + 1, first.offset);
    const inline = !/[\r\n]/.test(leading);
    const indent = inline ? '' : (indentationAt(text, first.offset) || `${closeIndent}  `);
    const property = `${JSON.stringify(key)}: ${serializeJsonValue(nested[key], inline, indent, eol)}`;
    const startsWithSpace = /^[\t ]/.test(text.slice(openOffset + 1, openOffset + 2));
    const suffix = inline && !startsWithSpace && text[openOffset + 1] !== undefined ? ' ' : '';
    const insertion = inline ? ` ${property},${suffix}` : `${eol}${indent}${property},`;
    return `${text.slice(0, openOffset + 1)}${insertion}${text.slice(openOffset + 1)}`;
  }

  const inner = text.slice(openOffset + 1, closeOffset);
  const commentEnd = lastCommentEnd(text, objectNode);
  const lineStyle = /[\r\n]/.test(inner);
  const property = `${JSON.stringify(key)}: ${serializeJsonValue(nested[key], !lineStyle, `${closeIndent}  `, eol)}`;

  if (commentEnd >= 0) {
    const suffix = text.slice(commentEnd, closeOffset);
    const insertion = lineStyle
      ? `${eol}${closeIndent}  ${property}${/[\r\n]/.test(suffix) ? '' : `${eol}${closeIndent}`}`
      : ` ${property}`;
    return `${text.slice(0, commentEnd)}${insertion}${text.slice(commentEnd)}`;
  }

  if (lineStyle) {
    const insertionOffset = closeOffset - closeIndent.length;
    const insertion = `${closeIndent}  ${property}${eol}`;
    return `${text.slice(0, insertionOffset)}${insertion}${text.slice(insertionOffset)}`;
  }

  return `${text.slice(0, closeOffset)}${property}${text.slice(closeOffset)}`;
}

function computeJsonCandidate(text, registrationPath, registration, format, existingTree = null) {
  try {
    if (!text) {
      const nested = valueForMissingPath(registrationPath, registration);
      return `${JSON.stringify(nested, null, 2)}\n`;
    }
    const tree = existingTree || parseJsonDocument(text, format, format === 'jsonc').tree;
    let objectNode = tree;
    let index = 0;
    for (; index < registrationPath.length - 1; index += 1) {
      const property = propertyChildren(objectNode).find(candidate => {
        const keyNode = candidate.children && candidate.children[0];
        return keyNode && keyNode.value === registrationPath[index];
      });
      if (!property) break;
      objectNode = property.children[1];
      if (!objectNode || objectNode.type !== 'object') {
        fail('The host config has a conflicting parent value for the Syntra registration.');
      }
    }
    if (!objectNode || objectNode.type !== 'object') {
      fail('The host config has a conflicting parent value for the Syntra registration.');
    }
    const remaining = registrationPath.slice(index);
    const nested = valueForMissingPath(remaining, registration);
    const insertionKey = remaining[0];
    const insertionValue = nested[insertionKey];
    return insertJsonProperty(text, objectNode, insertionKey, insertionValue);
  } catch (error) {
    if (error instanceof SetupError) throw error;
    fail(`The ${format.toUpperCase()} host config cannot be edited without ambiguity.`);
  }
}

// Recognize known argv layouts. Unknown wrappers and ambiguous host shapes
// stay under operator control even when --replace is supplied.
function identifyRegistration(registration, agent, platform) {
  if (!registration || typeof registration !== 'object') return null;
  if (agent === 'opencode') {
    if (registration.type !== 'local' || !Array.isArray(registration.command) || Object.hasOwn(registration, 'args')) return null;
  } else if (Array.isArray(registration.command) || (Object.hasOwn(registration, 'type') && registration.type !== 'stdio')) {
    return null;
  }

  let { command, args = [] } = registration;
  if (Array.isArray(command)) [command, ...args] = command;
  if (typeof command !== 'string' || !Array.isArray(args) ||
      args.some(arg => typeof arg !== 'string' || arg.includes('\0'))) return null;

  const paths = pathApi(platform);
  const basename = value => paths.basename(value).toLowerCase();
  let launcher = basename(command);
  if (['cmd', 'cmd.exe'].includes(launcher)) {
    if (args[0]?.toLowerCase() !== '/d' || args[1]?.toLowerCase() !== '/c' ||
        !['npx', 'npx.cmd'].includes(args[2]?.toLowerCase())) return null;
    launcher = 'npx';
    args = args.slice(3);
  }
  if (['node', 'node.exe'].includes(launcher)) {
    if (!paths.isAbsolute(command) || !paths.isAbsolute(args[0] ?? '')) return null;
    const script = args[0];
    args = args.slice(1);
    if (basename(script) === 'npx-cli.js' && /[\\/]npm[\\/]bin[\\/]npx-cli\.js$/.test(script)) {
      launcher = 'npx';
    } else if (basename(script) === 'launcher.cjs' && args[0] === 'mcp' &&
        /[\\/]node_modules[\\/]@synapsync[\\/]syntra-mcp[\\/]bin[\\/]launcher\.cjs$/.test(script)) {
      return { mode: 'local', launcher: 'node', node: command, script, tail: args.slice(1) };
    } else {
      return null;
    }
  }
  if (['npx', 'npx.cmd', 'npx.exe'].includes(launcher)) {
    let index = ['-y', '--yes'].includes(args[0]) ? 1 : 0;
    if (args[index] === '--prefer-online') index += 1;
    const match = /^@synapsync\/syntra-mcp(?:@([0-9A-Za-z][0-9A-Za-z.+-]*))?$/.exec(args[index] ?? '');
    if (!match || args[index + 1] !== 'mcp') return null;
    const pin = match[1];
    return { mode: pin === 'latest' ? 'latest' : pin ? 'pinned' : 'npm-unversioned',
      launcher, pin, tail: args.slice(index + 2) };
  }
  if (['syntra', 'syntra-mcp', 'syntra.exe', 'syntra-mcp.exe'].includes(command) && args[0] === 'mcp') {
    return { mode: 'direct', launcher, tail: args.slice(1) };
  }
  return null;
}

function planRegistration(existing, plan) {
  const identified = existing.exists && identifyRegistration(existing.value, plan.agent, plan.platform);
  if (existing.exists && !identified) {
    fail('A different Syntra host registration already exists and could not be identified as Syntra. Rename or remove that entry before running setup.');
  }
  plan.mode = plan.requestedMode || identified?.mode || 'latest';
  if (existing.exists && !plan.requestedMode) return { changed: false, status: 'matching' };
  const launch = shellCommand(plan.platform, plan.mode, plan.options);
  const tail = identified?.tail || [];
  const args = [...launch.args, ...tail];
  const fields = plan.agent === 'opencode' ? { command: [launch.command, ...args] } : { command: launch.command, args };
  const defaults = plan.agent === 'opencode' ? { type: 'local' } : plan.agent === 'claude-code' ? { type: 'stdio' } : {};
  plan.targetRegistration = { ...(existing.exists ? existing.value : defaults), ...fields };
  // Keep environment values and operator-supplied arguments out of previews.
  plan.previewCommand = [launch.command, ...launch.args];
  plan.tomlSnippet = [
    '[mcp_servers.syntra]',
    `command = ${tomlValue(launch.command)}`,
    `args = ${tomlValue(args)}`,
    '',
  ].join('\n');
  if (existing.exists && isDeepStrictEqual(normalizeTomlValue(existing.value), normalizeTomlValue(plan.targetRegistration))) {
    return { changed: false, status: 'matching' };
  }
  return { changed: true, status: existing.exists ? 'replace' : 'add',
    before: identified };
}

function jsonRegistrationPath(plan, value) {
  if (plan.agent !== 'opencode') return plan.registrationPath;
  const legacy = ['mcp', 'syntra'];
  if (!getOwnPath(value, legacy).exists) return plan.registrationPath;
  if (getOwnPath(value, plan.registrationPath).exists) {
    fail('OpenCode has both legacy and current Syntra registrations. Resolve the duplicate before using --replace.');
  }
  return legacy;
}

function previewJsonRegistration(config, plan, before) {
  const allowComments = plan.format === 'jsonc';
  if (before.tree) checkJsonParentTypes(before.tree, plan.registrationPath);
  const existing = before.tree
    ? getOwnPath(before.value, plan.registrationPath)
    : { exists: false, value: undefined };

  const registration = planRegistration(existing, plan);
  if (!registration.changed) return registration;
  let proposedText = config.text;
  if (existing.exists) {
    for (const key of plan.agent === 'opencode' ? ['command'] : ['command', 'args']) {
      const tree = parseJsonDocument(proposedText, plan.format, allowComments).tree;
      const node = jsonc.findNodeAtLocation(tree, [...plan.registrationPath, key]);
      const value = plan.targetRegistration[key];
      if (node) {
        proposedText = proposedText.slice(0, node.offset) + JSON.stringify(value) + proposedText.slice(node.offset + node.length);
      } else {
        const parent = jsonc.findNodeAtLocation(tree, plan.registrationPath);
        proposedText = insertJsonProperty(proposedText, parent, key, value);
      }
    }
  } else {
    proposedText = computeJsonCandidate(config.text, plan.registrationPath, plan.targetRegistration, plan.format, before.tree);
  }

  const after = parseJsonDocument(proposedText, plan.format, allowComments);
  const afterRegistration = getOwnPath(after.value, plan.registrationPath);
  if (!afterRegistration.exists || !isDeepStrictEqual(
    deepPlainCopy(afterRegistration.value),
    deepPlainCopy(plan.targetRegistration),
  )) {
    fail('The proposed host registration could not be validated.');
  }
  const oldUnrelated = removeRegistrationAndNewParents(before.value, plan.registrationPath, before.value);
  const newUnrelated = removeRegistrationAndNewParents(after.value, plan.registrationPath, before.value);
  if (!isDeepStrictEqual(oldUnrelated, newUnrelated)) {
    fail('The proposed host edit would alter unrelated settings.');
  }

  return { ...registration, proposedText };
}

function normalizeTomlValue(value) {
  if (Array.isArray(value)) return value.map(normalizeTomlValue);
  if (value instanceof Date) return value.toISOString();
  if (!value || typeof value !== 'object') return value;
  const normalized = {};
  for (const key of Object.keys(value).sort()) normalized[key] = normalizeTomlValue(value[key]);
  return normalized;
}

function tomlLineEnding(text) {
  return text.includes('\r\n') && !text.replace(/\r\n/g, '').includes('\n') ? '\r\n' : '\n';
}

function finishWithNewline(text, eol) {
  let end = text.length;
  while (end > 0 && ' \t\r\n'.includes(text[end - 1])) end -= 1;
  return text.slice(0, end) + eol;
}

function computeTomlCandidate(text, snippet) {
  const eol = tomlLineEnding(text);
  const prefix = /[^ \t\r\n]/.test(text) ? finishWithNewline(text, eol) + eol : '';
  return prefix + finishWithNewline(snippet.replace(/\r?\n/g, eol), eol);
}

function replaceTomlRegistration(text, plan, statements, inlineValues) {
  const keys = plan.registrationPath;
  const sameKeys = (left, right) => isDeepStrictEqual(left, right);
  const statementFor = field => statements.find(statement =>
    statement.valueStart !== undefined && sameKeys(statement.keys, field));
  let inlineStart = statementFor(keys)?.valueStart;
  if (inlineStart === undefined) {
    const parentStart = statementFor(keys.slice(0, -1))?.valueStart;
    inlineStart = inlineValues.find(value => value.tableStart === parentStart &&
      sameKeys(value.keys, keys.slice(-1)))?.start;
  }
  const edits = [];
  for (const key of ['command', 'args']) {
    const value = tomlValue(plan.targetRegistration[key]);
    if (inlineStart !== undefined) {
      const field = inlineValues.find(entry => entry.tableStart === inlineStart && sameKeys(entry.keys, [key]));
      if (field) edits.push({ start: field.start, end: field.end, value });
      else edits.push({ start: inlineStart + 1, end: inlineStart + 1, value: ` ${key} = ${value},` });
    } else {
      const field = statementFor([...keys, key]);
      if (!field) fail('The Syntra registration has an unsupported TOML launch field layout.');
      edits.push({ start: field.valueStart, end: field.end, value });
    }
  }
  for (const edit of edits.sort((left, right) => right.start - left.start)) {
    text = text.slice(0, edit.start) + edit.value + text.slice(edit.end);
  }
  return text;
}

function previewTomlRegistration(config, plan) {
  let before;
  const statements = [];
  const inlineValues = [];
  try {
    before = config.exists
      ? toml.parse(config.text, {
        onStatement: statement => statements.push(statement),
        onInlineValue: value => inlineValues.push(value),
      })
      : Object.create(null);
  } catch {
    fail('The TOML host config is malformed.');
  }
  const existing = getOwnPath(before, plan.registrationPath);
  const registration = planRegistration(existing, plan);
  if (!registration.changed) return registration;
  const proposedText = existing.exists
    ? replaceTomlRegistration(config.text, plan, statements, inlineValues)
    : computeTomlCandidate(config.text, plan.tomlSnippet);
  let after;
  try {
    after = toml.parse(proposedText);
  } catch {
    fail('The TOML host config cannot safely accept the Syntra table.');
  }
  const afterRegistration = getOwnPath(after, plan.registrationPath);
  if (!afterRegistration.exists || !isDeepStrictEqual(
    normalizeTomlValue(afterRegistration.value),
    normalizeTomlValue(plan.targetRegistration),
  )) {
    fail('The proposed TOML registration could not be validated.');
  }
  if (!isDeepStrictEqual(
    normalizeTomlValue(removeRegistrationAndNewParents(after, plan.registrationPath, before)),
    normalizeTomlValue(removeRegistrationAndNewParents(before, plan.registrationPath, before)),
  )) {
    fail('The proposed TOML edit would alter unrelated settings.');
  }
  return { ...registration, proposedText };
}

function buildPlan(agent, env = process.env, platform = process.platform, options = {}) {
  const plan = { ...hostPlan(agent, env, platform), requestedMode: options.mode, platform, options: { ...options, env } };
  inspectPath(plan.configPath, 'file', platform);
  inspectPath(plan.skillPath, 'directory', platform);
  const config = readConfig(plan.configPath, plan.format, platform);
  let registration;
  if (plan.format === 'toml') {
    registration = previewTomlRegistration(config, plan);
  } else {
    const document = config.exists
      ? parseJsonDocument(config.text, plan.format, plan.format === 'jsonc')
      : { tree: null, value: {} };
    plan.registrationPath = jsonRegistrationPath(plan, document.value);
    registration = previewJsonRegistration(config, plan, document);
  }
  const bundle = readBundledSkill();
  const skillStatus = inspectExistingSkill(plan.skillPath, bundle.files, platform);
  if (skillStatus === 'conflict' && registration.status === 'add') {
    fail('The existing Syntra skill differs from the bundled files; it was preserved.');
  }
  return {
    ...plan,
    configExists: config.exists,
    configSnapshot: config.snapshot,
    bundleFiles: bundle.files,
    registration,
    skillStatus,
  };
}

function renderPlan(plan, dryRun = true) {
  const lines = [
    `Syntra setup preview for ${plan.displayName}`,
    `Config: ${plan.configPath}${plan.configExists ? '' : ' (not created)'}`,
    `Bundled skill destination: ${plan.skillPath}`,
  ];

  if (plan.registration.status === 'matching') {
    lines.push(`MCP registration preserved (${plan.mode}).`);
  } else {
    lines.push(`Syntra MCP registration ${plan.registration.status === 'replace' ? 'migration' : 'addition'}: ${plan.registrationPath.join('.')}`);
    if (plan.registration.status === 'replace') {
      const before = plan.registration.before;
      const pin = before.pin && STABLE.test(before.pin) ? ` @${before.pin}` : '';
      lines.push(`Current mode: ${before.mode}${pin}. Existing MCP arguments and host settings will be preserved.`);
    }
    lines.push(`Proposed mode: ${plan.mode}`);
    lines.push(`Launch command: ${JSON.stringify(plan.previewCommand)}`);
  }

  if (plan.skillStatus === 'matching') {
    lines.push('Bundled skill: the destination already matches the packaged files.');
  } else if (plan.skillStatus === 'conflict') {
    lines.push('Bundled skill: the existing different files will be preserved.');
  } else {
    lines.push('+ install the bundled skill files (SKILL.md and references/tools.md).');
  }
  if (dryRun) lines.push('Dry run only: no files were changed.');
  return `${lines.join('\n')}\n`;
}

function readRegularSnapshot(targetPath, allowHardlinks = false) {
  let fd;
  try {
    const beforePath = fs.lstatSync(targetPath);
    if (beforePath.isSymbolicLink() || !beforePath.isFile() || (!allowHardlinks && beforePath.nlink > 1)) return null;
    const noFollow = fs.constants.O_NOFOLLOW || 0;
    fd = fs.openSync(targetPath, fs.constants.O_RDONLY | noFollow);
    const before = fs.fstatSync(fd);
    const bytes = fs.readFileSync(fd);
    const after = fs.fstatSync(fd);
    const afterPath = fs.lstatSync(targetPath);
    if (!sameFileObservation(before, after) || !sameFileObservation(after, afterPath)) return null;
    return { bytes, identity: fileObservation(after) };
  } catch {
    return null;
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch { /* The snapshot is discarded if closing is relevant. */ }
    }
  }
}

function safeRemoveOwnedFile(record, allowPrefix = false) {
  if (!record) return true;
  const snapshot = readRegularSnapshot(record.path, record.allowHardlinks === true);
  if (!snapshot || !sameFileIdentity(record.identity, snapshot.identity)) return false;
  const expected = record.bytes;
  const matches = snapshot.bytes.equals(expected) ||
    (allowPrefix && snapshot.bytes.length <= expected.length &&
      expected.subarray(0, snapshot.bytes.length).equals(snapshot.bytes));
  if (!matches) return false;
  try {
    fs.unlinkSync(record.path);
    return true;
  } catch {
    return false;
  }
}

function safeRemoveOwnedDirectory(record) {
  if (!record) return true;
  try {
    const stat = fs.lstatSync(record.path);
    if (stat.isSymbolicLink() || !stat.isDirectory() ||
        !sameFileIdentity(record.identity, fileObservation(stat))) return false;
    fs.rmdirSync(record.path);
    return true;
  } catch (error) {
    if (error && error.code === 'ENOENT') return true;
    return false;
  }
}

function createExclusiveFile(targetPath, bytes, mode, platform) {
  const expected = Buffer.from(bytes);
  let fd;
  let identity = null;
  try {
    const noFollow = fs.constants.O_NOFOLLOW || 0;
    fd = fs.openSync(
      targetPath,
      fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | noFollow,
      mode,
    );
    identity = fileObservation(fs.fstatSync(fd));
    if (platform !== 'win32') fs.fchmodSync(fd, mode & 0o777);
    writeAll(fd, expected);
    fs.fsyncSync(fd);
    const stat = fs.fstatSync(fd);
    if (!sameFileIdentity(identity, fileObservation(stat)) || stat.size !== expected.length) {
      fail('A setup file changed while it was being written.');
    }
    identity = fileObservation(stat);
    return { path: targetPath, identity, bytes: expected, mode: mode & 0o777 };
  } catch (error) {
    if (identity) safeRemoveOwnedFile({ path: targetPath, identity, bytes: expected }, true);
    if (error instanceof SetupError) throw error;
    fail('A private setup file could not be created safely.');
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch { /* The operation result has already been determined. */ }
    }
  }
}

function createExclusiveDirectory(targetPath, mode, platform) {
  let created = null;
  try {
    fs.mkdirSync(targetPath, { mode });
    const initial = fs.lstatSync(targetPath);
    if (initial.isSymbolicLink() || !initial.isDirectory()) fail('A setup directory was replaced unexpectedly.');
    const identity = fileObservation(initial);
    created = { path: targetPath, identity };
    if (platform !== 'win32') fs.chmodSync(targetPath, mode & 0o777);
    const finalStat = fs.lstatSync(targetPath);
    if (!sameFileIdentity(identity, fileObservation(finalStat)) || !finalStat.isDirectory()) {
      fail('A setup directory changed while it was being created.');
    }
    return { path: targetPath, identity: fileObservation(finalStat) };
  } catch (error) {
    if (created) safeRemoveOwnedDirectory(created);
    if (error instanceof SetupError) throw error;
    if (error && error.code === 'EEXIST') fail('A setup destination appeared during installation.');
    fail('A setup directory could not be created safely.');
  }
}

function syncDirectory(targetPath, platform) {
  if (platform === 'win32') return false;
  let fd;
  try {
    const directoryFlag = fs.constants.O_DIRECTORY || 0;
    fd = fs.openSync(targetPath, fs.constants.O_RDONLY | directoryFlag);
    fs.fsyncSync(fd);
    return true;
  } catch (error) {
    if (error && ['EINVAL', 'ENOTSUP', 'EISDIR', 'EBADF'].includes(error.code)) return false;
    throw error;
  } finally {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch { /* Directory durability is best-effort on supported hosts. */ }
    }
  }
}

function createSkillStage(plan, token, platform) {
  const paths = pathApi(platform);
  const parent = paths.dirname(plan.skillPath);
  const rootPath = paths.join(parent, `.syntra-skill-stage-${token}`);
  const stage = { root: null, directories: [], files: [] };
  try {
    stage.root = createExclusiveDirectory(rootPath, 0o700, platform);
    const referencesPath = paths.join(rootPath, 'references');
    stage.directories.push(createExclusiveDirectory(referencesPath, 0o755, platform));
    for (const [relative, bytes] of plan.bundleFiles) {
      const target = paths.join(rootPath, ...relative.split('/'));
      stage.files.push(createExclusiveFile(target, bytes, 0o644, platform));
    }
    return stage;
  } catch (error) {
    cleanupSkillStage(stage);
    throw error;
  }
}

function cleanupSkillStage(stage) {
  if (!stage) return true;
  let clean = true;
  for (const file of [...stage.files].reverse()) {
    if (!safeRemoveOwnedFile(file)) clean = false;
  }
  for (const directory of [...stage.directories].reverse()) {
    if (!safeRemoveOwnedDirectory(directory)) clean = false;
  }
  if (stage.root && !safeRemoveOwnedDirectory(stage.root)) clean = false;
  return clean;
}

function installStagedSkill(plan, stage, platform) {
  const paths = pathApi(platform);
  const installed = { directories: [], files: [] };
  const current = inspectExistingSkill(plan.skillPath, plan.bundleFiles, platform);
  if (current === 'matching') return installed;
  if (current !== 'absent') fail('The Syntra skill destination changed and was preserved.');

  try {
    const root = createExclusiveDirectory(plan.skillPath, 0o755, platform);
    installed.directories.push(root);
    const referencesPath = paths.join(plan.skillPath, 'references');
    installed.directories.push(createExclusiveDirectory(referencesPath, 0o755, platform));
    for (const [relative, bytes] of plan.bundleFiles) {
      const staged = stage.files.find(file => file.path === paths.join(stage.root.path, ...relative.split('/')));
      if (!staged) fail('The staged Syntra skill is incomplete.');
      const destination = paths.join(plan.skillPath, ...relative.split('/'));
      fs.linkSync(staged.path, destination);
      staged.allowHardlinks = true;
      const stat = fs.lstatSync(destination);
      if (stat.isSymbolicLink() || !stat.isFile() || stat.size !== bytes.length) {
        fail('A bundled skill file changed while it was being installed.');
      }
      installed.files.push({
        path: destination,
        identity: fileObservation(stat),
        bytes,
        allowHardlinks: true,
      });
    }
    for (const file of stage.files) {
      if (!safeRemoveOwnedFile(file)) fail('A staged Syntra skill file could not be removed safely.');
    }
    if (!safeRemoveOwnedDirectory(stage.directories[0]) || !safeRemoveOwnedDirectory(stage.root)) {
      fail('A staged Syntra skill directory could not be removed safely.');
    }
    stage.files = [];
    stage.directories = [];
    stage.root = null;
    if (plan.skillStatus === 'conflict') return;
  if (inspectExistingSkill(plan.skillPath, plan.bundleFiles, platform) !== 'matching') {
      fail('The installed Syntra skill did not match its packaged files.');
    }
    return installed;
  } catch (error) {
    error.setupInstalledSkill = installed;
    throw error;
  }
}

function rollbackInstalledSkill(installed) {
  if (!installed) return true;
  let safe = true;
  for (const file of [...installed.files].reverse()) {
    if (!safeRemoveOwnedFile(file)) safe = false;
  }
  for (const directory of [...installed.directories].reverse()) {
    if (!safeRemoveOwnedDirectory(directory)) safe = false;
  }
  return safe;
}

function acquireSetupLock(lockPath, agent, platform) {
  try {
    const existing = fs.lstatSync(lockPath);
    if (existing.isSymbolicLink()) fail('The Syntra setup lock path is a symbolic link.');
    fail(`A Syntra setup lock already exists next to the selected host config. Confirm no setup is active, then remove this stale lock manually before retrying.`);
  } catch (error) {
    if (error instanceof SetupError) throw error;
    if (!error || error.code !== 'ENOENT') fail('The Syntra setup lock could not be inspected safely.');
  }

  let fd;
  let identity = null;
  const token = randomUUID();
  const ownerBytes = Buffer.from(`${JSON.stringify({ version: 1, token, pid: process.pid, agent, startedAt: new Date().toISOString() })}\n`);
  try {
    const noFollow = fs.constants.O_NOFOLLOW || 0;
    fd = fs.openSync(
      lockPath,
      fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | noFollow,
      0o600,
    );
    identity = fileObservation(fs.fstatSync(fd));
    if (platform !== 'win32') fs.fchmodSync(fd, 0o600);
    writeAll(fd, ownerBytes);
    fs.fsyncSync(fd);
    identity = fileObservation(fs.fstatSync(fd));
    return { path: lockPath, fd, identity, token, ownerBytes };
  } catch (error) {
    if (fd !== undefined) {
      if (identity) safeRemoveOwnedFile({ path: lockPath, identity, bytes: ownerBytes }, true);
      try { fs.closeSync(fd); } catch { /* The lock creation failure is reported below. */ }
    }
    if (error instanceof SetupError) throw error;
    if (error && error.code === 'EEXIST') {
      fail(`A Syntra setup lock already exists next to the selected host config. Confirm no setup is active, then remove this stale lock manually before retrying.`);
    }
    fail('A Syntra setup lock could not be created safely.');
  }
}

function releaseSetupLock(lock, platform) {
  if (!lock) return true;
  let released = false;
  try {
    const current = readRegularSnapshot(lock.path);
    if (!current || !sameFileIdentity(lock.identity, current.identity)) return false;
    let owner;
    try { owner = JSON.parse(current.bytes.toString('utf8')); } catch { return false; }
    if (!owner || owner.token !== lock.token) return false;
    fs.unlinkSync(lock.path);
    released = true;
    syncDirectory(path.dirname(lock.path), platform);
    return true;
  } catch {
    return false;
  } finally {
    try { fs.closeSync(lock.fd); } catch { /* The lock path has already been handled. */ }
    if (!released) {
      // The lock is intentionally left in place if its identity or owner cannot be verified.
    }
  }
}

function createBackup(plan, token, platform) {
  if (!plan.configSnapshot.exists) return null;
  const paths = pathApi(platform);
  const targetPath = paths.join(
    paths.dirname(plan.configPath),
    `.${paths.basename(plan.configPath)}.syntra-backup-${token}`,
  );
  return createExclusiveFile(targetPath, plan.configSnapshot.bytes, 0o600, platform);
}

function configCandidate(plan) {
  return plan.registration.changed ? Buffer.from(plan.registration.proposedText, 'utf8') : null;
}

function assertConfigUnchanged(plan, platform) {
  const current = readConfig(plan.configPath, plan.format, platform).snapshot;
  if (!sameConfigSnapshot(plan.configSnapshot, current)) {
    fail('The host config changed during setup; the concurrent edit was preserved. Retry the setup command.');
  }
}

function assertInstalledSkill(plan, platform) {
  inspectPath(plan.skillPath, 'directory', platform);
  if (plan.skillStatus === 'conflict') return;
  if (inspectExistingSkill(plan.skillPath, plan.bundleFiles, platform) !== 'matching') {
    fail('The bundled skill changed during setup; it was preserved and the registration was not committed.');
  }
}

function assertBackupIntact(backup, plan, platform) {
  if (!plan.configSnapshot.exists) {
    if (backup) fail('An unexpected setup backup was created.');
    return;
  }
  const snapshot = backup && readRegularSnapshot(backup.path);
  const privateMode = platform === 'win32' || (snapshot && snapshot.identity.mode === 0o600);
  if (!snapshot || !sameFileIdentity(backup.identity, snapshot.identity) ||
      !isDeepStrictEqual(backup.identity, snapshot.identity) ||
      !snapshot.bytes.equals(plan.configSnapshot.bytes) || !privateMode) {
    fail('The private config backup changed during setup; it was preserved and the registration was not committed.');
  }
}

function publishConfig(plan, staged, platform, onValidated) {
  inspectPath(plan.configPath, 'file', platform);
  assertConfigUnchanged(plan, platform);
  if (typeof onValidated === 'function') onValidated();
  if (plan.configSnapshot.exists) {
    fs.renameSync(staged.path, plan.configPath);
  } else {
    fs.linkSync(staged.path, plan.configPath);
    staged.allowHardlinks = true;
  }
}

function applySetup(agent, options = {}) {
  const env = options.env || process.env;
  const platform = options.platform || process.platform;
  const testHooks = options.testHooks || Object.create(null);
  const initialPlan = options.preview || buildPlan(agent, env, platform, options);
  if (initialPlan.registration.status === 'replace' && !options.replace) refuseReplacement(agent, initialPlan.requestedMode);
  const initiallyChanged = initialPlan.registration.changed || initialPlan.skillStatus === 'absent';
  if (!initiallyChanged) {
    return { plan: initialPlan, configChanged: false, skillInstalled: false, backupPath: null, unchanged: true };
  }

  const paths = pathApi(platform);
  const createdDirectories = [];
  const lockPath = `${initialPlan.configPath}.syntra.lock`;
  let lock = null;
  let stagedConfig = null;
  let stagedSkill = null;
  let backup = null;
  let installedSkill = null;
  let configCommitted = false;
  let configVerified = false;
  let keepLock = false;
  let result = null;

  const phase = (name, details) => {
    if (typeof testHooks.onPhase === 'function') testHooks.onPhase(name, details);
  };

  try {
    ensureDirectoryChain(paths.dirname(lockPath), platform, createdDirectories);
    lock = acquireSetupLock(lockPath, agent, platform);
    syncDirectory(paths.dirname(lockPath), platform);
    phase('lock-acquired', { lockPath });

    const plan = buildPlan(agent, env, platform, options);
    if ((initialPlan.registration.status === 'replace' || plan.registration.status === 'replace') &&
        (plan.configPath !== initialPlan.configPath || !sameConfigSnapshot(initialPlan.configSnapshot, plan.configSnapshot) ||
         !isDeepStrictEqual(plan.targetRegistration, initialPlan.targetRegistration))) {
      fail('The host config changed after the setup preview. Run setup again to review the new entry.');
    }
    if (plan.registration.status === 'replace' && !options.replace) refuseReplacement(agent, initialPlan.requestedMode);
    if (!plan.registration.changed && plan.skillStatus === 'matching') {
      result = { plan, configChanged: false, skillInstalled: false, backupPath: null, unchanged: true };
      return result;
    }

    if (plan.skillStatus === 'absent') {
      ensureDirectoryChain(paths.dirname(plan.skillPath), platform, createdDirectories);
      stagedSkill = createSkillStage(plan, lock.token, platform);
    }

    const candidate = configCandidate(plan);
    if (candidate) {
      const tempPath = paths.join(paths.dirname(plan.configPath), `.${paths.basename(plan.configPath)}.syntra-tmp-${lock.token}`);
      const mode = plan.configSnapshot.exists ? plan.configSnapshot.mode : 0o600;
      stagedConfig = createExclusiveFile(tempPath, candidate, mode, platform);
    }
    phase('staged', { configChanged: Boolean(candidate), skillInstall: Boolean(stagedSkill) });

    if (candidate) {
      assertConfigUnchanged(plan, platform);
      backup = createBackup(plan, lock.token, platform);
      if (backup) syncDirectory(paths.dirname(backup.path), platform);
      phase('backup-created', { backupPath: backup && backup.path });
      assertBackupIntact(backup, plan, platform);
    }

    if (stagedSkill) {
      try {
        installedSkill = installStagedSkill(plan, stagedSkill, platform);
      } catch (error) {
        installedSkill = error && error.setupInstalledSkill;
        throw error;
      }
      phase('skill-installed', { skillPath: plan.skillPath });
      assertInstalledSkill(plan, platform);
    }

    if (candidate) {
      phase('before-config-publish', { configPath: plan.configPath });
      publishConfig(plan, stagedConfig, platform, () => {
        phase('config-validated', { configPath: plan.configPath });
        assertInstalledSkill(plan, platform);
        assertBackupIntact(backup, plan, platform);
        if (plan.configSnapshot.exists) assertConfigUnchanged(plan, platform);
      });
      configCommitted = true;
      const published = readRegularSnapshot(plan.configPath, !plan.configSnapshot.exists);
      if (!published || !published.bytes.equals(stagedConfig.bytes)) {
        keepLock = true;
        fail('The host config could not be verified after replacement; its backup was retained.');
      }
      configVerified = true;
      syncDirectory(paths.dirname(plan.configPath), platform);
      phase('config-published', { configPath: plan.configPath, backupPath: backup && backup.path });
    }

    result = {
      plan,
      configChanged: Boolean(candidate),
      skillInstalled: Boolean(installedSkill && installedSkill.directories.length),
      backupPath: backup && backup.path,
      unchanged: false,
    };
    return result;
  } catch (error) {
    let rollbackSafe = true;
    if (!configCommitted) {
      if (installedSkill && !rollbackInstalledSkill(installedSkill)) rollbackSafe = false;
      if (stagedSkill && !cleanupSkillStage(stagedSkill)) rollbackSafe = false;
      if (stagedConfig && !safeRemoveOwnedFile(stagedConfig)) rollbackSafe = false;
      if (rollbackSafe && backup && !safeRemoveOwnedFile(backup)) rollbackSafe = false;
      if (!rollbackSafe) keepLock = true;
    } else {
      keepLock = !configVerified;
    }

    if (!rollbackSafe) {
      fail(`Setup stopped after a filesystem change it could not safely roll back. The lock remains next to the selected host config; confirm no setup is active, inspect the config, backup, and skill, then remove the lock manually before retrying.`);
    }
    if (error instanceof SetupError) throw error;
    fail(configCommitted
      ? 'Setup changes were applied, but a final filesystem sync failed. The backup was retained; rerun setup to verify.'
      : 'Setup could not be completed safely; any concurrent user changes were preserved.');
  } finally {
    if (stagedSkill && !configCommitted) cleanupSkillStage(stagedSkill);
    if (stagedConfig && configCommitted) safeRemoveOwnedFile(stagedConfig);
    if (lock) {
      if (keepLock) {
        try { fs.closeSync(lock.fd); } catch { /* A retained stale lock needs manual recovery. */ }
      } else if (!releaseSetupLock(lock, platform)) {
        // An unverified lock is retained rather than risking another process's lock.
      }
    }
    removeCreatedDirectories(createdDirectories);
  }
}

function renderApplied(result) {
  const lines = [`Syntra setup complete for ${result.plan.displayName}`];
  if (result.unchanged) {
    lines.push(result.plan.skillStatus === 'conflict'
      ? `MCP registration already matches. Existing skill preserved: ${result.plan.skillPath}`
      : 'MCP registration and bundled skill already match the packaged files.');
    lines.push('No files were changed.');
  } else {
    if (result.configChanged) {
      lines.push(`MCP registration updated: ${result.plan.configPath}`);
      if (result.backupPath) lines.push(`Private config backup: ${result.backupPath}`);
    } else {
      lines.push('MCP registration already matches.');
    }
    lines.push(result.skillInstalled
      ? `Bundled skill installed: ${result.plan.skillPath}`
      : result.plan.skillStatus === 'conflict'
        ? `Existing skill preserved: ${result.plan.skillPath}`
        : `Bundled skill already matches: ${result.plan.skillPath}`);
  }
  lines.push(`First time on this machine? Run: npx -y ${PACKAGE}@latest login`);
  lines.push('Approve the permissions you need in the browser. Agent sessions using the default Syntra profile share that login.');
  lines.push(`To check permissions and expiration, run: npx -y ${PACKAGE}@latest status`);
  lines.push('Start a new agent session to load this registration.');
  return `${lines.join('\n')}\n`;
}

function refuseReplacement(agent, mode) {
  fail(`Confirm the Syntra registration migration in a terminal, or run: syntra setup --agent ${agent} --mode ${mode} --replace`);
}

function runSetup(args, options = {}) {
  const parsed = parseSetupArgs(args);
  const plan = buildPlan(parsed.agent, options.env, options.platform, { ...options, mode: parsed.mode });
  if (parsed.dryRun) return renderPlan(plan);
  if (plan.registration.status === 'replace' && !parsed.replace) refuseReplacement(parsed.agent, parsed.mode);
  const applied = applySetup(parsed.agent, { ...options, mode: parsed.mode, preview: plan, replace: parsed.replace });
  return (plan.registration.status === 'replace' ? renderPlan(plan, false) : '') + renderApplied(applied);
}

async function setupCLI(args, options = {}) {
  const out = options.out || (message => process.stdout.write(message));
  if (args.length === 1 && ['--help', '-h'].includes(args[0])) {
    out('Usage: syntra setup --agent codex|claude-code|opencode [--mode latest|local] [--dry-run] [--replace]\n' +
      'New registrations default to latest. Choose --mode to migrate an existing registration.\n');
    return;
  }
  const parsed = parseSetupArgs(args);
  const plan = buildPlan(parsed.agent, options.env, options.platform, { ...options, mode: parsed.mode });
  if (parsed.dryRun) {
    out(renderPlan(plan));
    return;
  }
  if (plan.registration.status === 'replace') {
    out(renderPlan(plan, false));
    if (!parsed.replace) {
      const tty = options.tty ?? Boolean(process.stdin.isTTY && process.stdout.isTTY);
      if (!tty) refuseReplacement(parsed.agent, parsed.mode);
      const prompt = options.prompt || (async () => {
        const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
        try { return await rl.question('Replace this Syntra registration? [y/N] '); } finally { rl.close(); }
      });
      const answer = await prompt();
      if (!/^y(?:es)?$/i.test(answer.trim())) {
        out('Setup cancelled. No files were changed.\n');
        return;
      }
    }
  }
  out(renderApplied(applySetup(parsed.agent, { ...options, mode: parsed.mode, preview: plan, replace: true })));
}

function registrationState(agent, env = process.env, platform = process.platform, executingPackage = null) {
  const plan = hostPlan(agent, env, platform);
  const config = readConfig(plan.configPath, plan.format, platform);
  if (!config.exists) return { mode: 'absent' };
  const value = plan.format === 'toml'
    ? toml.parse(config.text)
    : parseJsonDocument(config.text, plan.format, plan.format === 'jsonc').value;
  const entry = getOwnPath(value, jsonRegistrationPath(plan, value));
  if (!entry.exists) return { mode: 'absent' };
  const state = identifyRegistration(entry.value, agent, platform);
  if (!state) return { mode: 'unresolved' };
  if (state.mode !== 'local') return { mode: state.mode, pin: state.pin };
  const node = realPath(state.node);
  const launcher = realPath(state.script);
  const current = executingPackage && realPath(path.join(executingPackage, 'bin', 'launcher.cjs'));
  if (!node || !launcher) return { mode: 'local', target: 'unresolved' };
  return { mode: 'local', target: current && samePath(current, launcher) ? 'current' : 'other' };
}

if (require.main === module) {
  setupCLI(process.argv.slice(2)).catch(error => {
    const message = error instanceof SetupError ? error.message : 'Setup could not be completed safely.';
    process.stderr.write(`syntra: ${message}\n`);
    process.exitCode = 1;
  });
}

module.exports = {
  AGENT_NAMES,
  SetupError,
  applySetup,
  buildPlan,
  computeJsonCandidate,
  computeTomlCandidate,
  hostPlan,
  inspectPath,
  parseSetupArgs,
  registrationState,
  renderPlan,
  runSetup,
  setupCLI,
  shellCommand,
};
