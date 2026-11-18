'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { randomUUID } = require('node:crypto');
const { isDeepStrictEqual, TextDecoder } = require('node:util');
const jsonc = require('./vendor/jsonc-parser/lib/umd/main.js');
const toml = require('./vendor/smol-toml/index.cjs');
const packageManifest = require('../package.json');

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
    } else {
      fail('Unknown setup option. Use --agent and --dry-run only.');
    }
  }

  if (!agent) fail('Choose a host with --agent codex, --agent claude-code, or --agent opencode.');
  if (!AGENT_NAMES.includes(agent)) fail('Supported setup hosts are codex, claude-code, and opencode.');
  return { agent, dryRun };
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

function shellCommand(platform) {
  const version = packageManifest.version;
  if (packageManifest.name !== '@synapsync/syntra' || typeof packageManifest.private !== 'boolean' ||
      typeof version !== 'string' || !/^[0-9A-Za-z][0-9A-Za-z.+-]*$/.test(version)) {
    fail('The Syntra package manifest has invalid setup metadata.');
  }
  const pinnedPackage = `@synapsync/syntra@${version}`;
  if (platform === 'win32') {
    // npx is a Windows .cmd entry point. cmd /d /c is the documented MCP wrapper;
    // every argument is a fixed token and no user input is interpolated.
    return Object.freeze({
      command: 'cmd.exe',
      args: Object.freeze(['/d', '/c', 'npx', '-y', pinnedPackage, 'mcp']),
    });
  }
  return Object.freeze({
    command: 'npx',
    args: Object.freeze(['-y', pinnedPackage, 'mcp']),
  });
}

function tomlValue(value) {
  if (Array.isArray(value)) return `[${value.map(item => JSON.stringify(item)).join(', ')}]`;
  return JSON.stringify(value);
}

function hostPlan(agent, env = process.env, platform = process.platform) {
  const paths = pathApi(platform);
  const home = homeDirectory(env, platform, paths);
  const command = shellCommand(platform);

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
      registration: { command: command.command, args: command.args },
      registrationPath: ['mcp_servers', 'syntra'],
      tomlSnippet: [
        '[mcp_servers.syntra]',
        `command = ${tomlValue(command.command)}`,
        `args = ${tomlValue(command.args)}`,
        '',
      ].join('\n'),
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
      registration: { type: 'stdio', command: command.command, args: command.args },
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
      registration: { type: 'local', command: command.args
        ? [command.command, ...command.args]
        : [command.command] },
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

function previewJsonRegistration(config, plan) {
  const allowComments = plan.format === 'jsonc';
  const before = config.exists
    ? parseJsonDocument(config.text, plan.format, allowComments)
    : { tree: null, value: {} };
  if (before.tree) checkJsonParentTypes(before.tree, plan.registrationPath);
  const existing = before.tree
    ? getOwnPath(before.value, plan.registrationPath)
    : { exists: false, value: undefined };

  if (existing.exists) {
    if (!isDeepStrictEqual(deepPlainCopy(existing.value), deepPlainCopy(plan.registration))) {
      fail('A different Syntra host registration already exists; it was not changed.');
    }
    return { changed: false, status: 'matching', safeDelta: null };
  }

  const proposedText = computeJsonCandidate(
    config.text,
    plan.registrationPath,
    plan.registration,
    plan.format,
    before.tree,
  );

  const after = parseJsonDocument(proposedText, plan.format, allowComments);
  const afterRegistration = getOwnPath(after.value, plan.registrationPath);
  if (!afterRegistration.exists || !isDeepStrictEqual(
    deepPlainCopy(afterRegistration.value),
    deepPlainCopy(plan.registration),
  )) {
    fail('The proposed host registration could not be validated.');
  }
  const oldUnrelated = removeRegistrationAndNewParents(before.value, plan.registrationPath, before.value);
  const newUnrelated = removeRegistrationAndNewParents(after.value, plan.registrationPath, before.value);
  if (!isDeepStrictEqual(oldUnrelated, newUnrelated)) {
    fail('The proposed host edit would alter unrelated settings.');
  }

  return {
    changed: true,
    status: 'add',
    safeDelta: {
      path: plan.registrationPath.join('.'),
      value: plan.registration,
    },
  };
}

function normalizeTomlValue(value) {
  if (Array.isArray(value)) return value.map(normalizeTomlValue);
  if (value instanceof Date) return value.toISOString();
  if (!value || typeof value !== 'object') return value;
  const normalized = {};
  for (const key of Object.keys(value).sort()) normalized[key] = normalizeTomlValue(value[key]);
  return normalized;
}

function computeTomlCandidate(text, snippet) {
  const remainder = text.replace(/\r\n/g, '');
  const lineEnding = text.includes('\r\n') && !remainder.includes('\n') ? '\r\n' : '\n';
  const separator = !text ? '' : (text.endsWith('\n') || text.endsWith('\r') ? '' : lineEnding);
  const formattedSnippet = snippet.replace(/\r\n|\n/g, lineEnding);
  return `${text}${separator}${formattedSnippet}`;
}

function previewTomlRegistration(config, plan) {
  let before;
  try {
    before = config.exists ? toml.parse(config.text) : Object.create(null);
  } catch {
    fail('The TOML host config is malformed.');
  }
  const existing = getOwnPath(before, plan.registrationPath);
  if (existing.exists) {
    if (!isDeepStrictEqual(normalizeTomlValue(existing.value), normalizeTomlValue(plan.registration))) {
      fail('A different Syntra host registration already exists; it was not changed.');
    }
    return { changed: false, status: 'matching', safeDelta: null };
  }

  const snippet = plan.tomlSnippet;
  const proposedText = computeTomlCandidate(config.text, snippet);
  let after;
  try {
    after = toml.parse(proposedText);
  } catch {
    fail('The TOML host config cannot safely accept the Syntra table.');
  }
  const afterRegistration = getOwnPath(after, plan.registrationPath);
  if (!afterRegistration.exists || !isDeepStrictEqual(
    normalizeTomlValue(afterRegistration.value),
    normalizeTomlValue(plan.registration),
  )) {
    fail('The proposed TOML registration could not be validated.');
  }
  if (!isDeepStrictEqual(
    normalizeTomlValue(removeRegistrationAndNewParents(after, plan.registrationPath, before)),
    normalizeTomlValue(removeRegistrationAndNewParents(before, plan.registrationPath, before)),
  )) {
    fail('The proposed TOML edit would alter unrelated settings.');
  }
  return {
    changed: true,
    status: 'add',
    safeDelta: { path: plan.registrationPath.join('.'), toml: snippet },
  };
}

function buildPlan(agent, env = process.env, platform = process.platform) {
  const plan = hostPlan(agent, env, platform);
  inspectPath(plan.configPath, 'file', platform);
  inspectPath(plan.skillPath, 'directory', platform);
  const config = readConfig(plan.configPath, plan.format, platform);
  const bundle = readBundledSkill();
  const skillStatus = inspectExistingSkill(plan.skillPath, bundle.files, platform);
  if (skillStatus === 'conflict') {
    fail('The existing Syntra skill differs from the bundled files; it was preserved.');
  }
  const registration = plan.format === 'toml'
    ? previewTomlRegistration(config, plan)
    : previewJsonRegistration(config, plan);
  return {
    ...plan,
    configExists: config.exists,
    configSnapshot: config.snapshot,
    bundleFiles: bundle.files,
    registration,
    skillStatus,
  };
}

function renderPlan(plan) {
  const lines = [
    `Syntra setup preview for ${plan.displayName}`,
    `Config: ${plan.configPath}${plan.configExists ? '' : ' (not created)'}`,
    `Bundled skill destination: ${plan.skillPath}`,
  ];

  if (plan.registration.status === 'matching') {
    lines.push('MCP registration: the exact pinned Syntra entry is already present.');
  } else {
    lines.push('Proposed Syntra-only config change (unrelated settings are omitted):');
    if (plan.registration.safeDelta.toml) {
      for (const line of plan.registration.safeDelta.toml.trimEnd().split('\n')) lines.push(`+ ${line}`);
    } else {
      lines.push(`+ ${plan.registration.safeDelta.path} = ${JSON.stringify(plan.registration.safeDelta.value, null, 2)}`);
    }
  }

  if (plan.skillStatus === 'matching') {
    lines.push('Bundled skill: the destination already matches the packaged files.');
  } else {
    lines.push('+ install the bundled skill files (SKILL.md and references/tools.md).');
  }
  lines.push('Dry run only: no files were changed.');
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
    fail(`A Syntra setup lock already exists at ${lockPath}. Confirm no setup is active, then remove this stale lock manually before retrying.`);
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
      fail(`A Syntra setup lock already exists at ${lockPath}. Confirm no setup is active, then remove this stale lock manually before retrying.`);
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
  if (!plan.registration.changed) return null;
  const text = plan.format === 'toml'
    ? computeTomlCandidate(plan.configSnapshot.exists ? new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(plan.configSnapshot.bytes) : '', plan.tomlSnippet)
    : computeJsonCandidate(
      plan.configSnapshot.exists ? new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(plan.configSnapshot.bytes) : '',
      plan.registrationPath,
      plan.registration.safeDelta.value,
      plan.format,
    );
  return Buffer.from(text, 'utf8');
}

function assertConfigUnchanged(plan, platform) {
  const current = readConfig(plan.configPath, plan.format, platform).snapshot;
  if (!sameConfigSnapshot(plan.configSnapshot, current)) {
    fail('The host config changed during setup; the concurrent edit was preserved. Retry the setup command.');
  }
}

function assertInstalledSkill(plan, platform) {
  inspectPath(plan.skillPath, 'directory', platform);
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
  const initialPlan = buildPlan(agent, env, platform);
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

    const plan = buildPlan(agent, env, platform);
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
      fail(`Setup stopped after a filesystem change it could not safely roll back. The lock remains at ${lockPath}; confirm no setup is active, inspect the config, backup, and skill, then remove the lock manually before retrying.`);
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
    lines.push('MCP registration and bundled skill already match the packaged files.');
    lines.push('No files were changed.');
    return `${lines.join('\n')}\n`;
  }
  if (result.configChanged) {
    lines.push(`MCP registration updated: ${result.plan.configPath}`);
    if (result.backupPath) lines.push(`Private config backup: ${result.backupPath}`);
  } else {
    lines.push('MCP registration already matches.');
  }
  lines.push(result.skillInstalled
    ? `Bundled skill installed: ${result.plan.skillPath}`
    : `Bundled skill already matches: ${result.plan.skillPath}`);
  return `${lines.join('\n')}\n`;
}

function runSetup(args, options = {}) {
  const parsed = parseSetupArgs(args);
  const env = options.env || process.env;
  const platform = options.platform || process.platform;
  if (parsed.dryRun) return renderPlan(buildPlan(parsed.agent, env, platform));
  return renderApplied(applySetup(parsed.agent, { ...options, env, platform }));
}

if (require.main === module) {
  try {
    process.stdout.write(runSetup(process.argv.slice(2)));
  } catch (error) {
    const message = error instanceof SetupError ? error.message : 'Setup preview could not be completed safely.';
    process.stderr.write(`syntra: ${message}\n`);
    process.exitCode = 1;
  }
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
  renderPlan,
  runSetup,
  shellCommand,
};
