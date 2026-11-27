# Syntra MCP

Give your coding agent access to the work you share in Syntra. Look up
workspaces, projects, teams and thread history, then prepare a private message
or send it when you explicitly ask. The package includes the `syntra` CLI and a
local MCP server that connects to the Syntra API over your authorized profile.

[Documentation](https://syntra.synapsynclabs.com/docs/) · [npm package](https://www.npmjs.com/package/@synapsync/syntra-mcp) · [Tool reference](https://syntra.synapsynclabs.com/docs/reference/tools.html)

## Quick start

Requires Node.js 22.12 or newer. npm installs the matching native executable
for Linux or macOS (x64 and arm64), or Windows (x64).

**1. Install and connect your account.** Run these commands in a terminal and
approve the connection in your browser.

```sh
npm install --global @synapsync/syntra-mcp
syntra login
syntra workspaces
```

**2. Register your host.** Run the command for the host you use.

| Host | Command |
| --- | --- |
| Codex | `syntra setup --agent codex` |
| Claude Code | `syntra setup --agent claude-code` |
| OpenCode | `syntra setup --agent opencode` |

Add `--dry-run` to preview changes. For Claude Desktop and other local stdio
hosts, use the [manual configuration templates](https://syntra.synapsynclabs.com/docs/agent-setup/host-configurations.html).

**3. Open a fresh agent session.** Ask it to list your Syntra workspaces.
The CLI and MCP share one login when they use the same OS user and profile.
For first-time setup without a global install, use
`npx -y @synapsync/syntra-mcp@latest` in place of the `syntra` command.

## Work with threads

| Ask your agent to… | MCP tools |
| --- | --- |
| Show the projects and teams in a workspace | `workspaces`, `projects`, `teams` |
| Find a teammate or read the latest replies in a thread | `members`, `resolve`, `threads`, `thread`, `messages` |
| Prepare a reply for you to review | `prepare` |
| Fork a published thread from a message (PUBLISH) | `fork` |
| Send the exact message you authorized | `publish` |

You can also use the CLI directly. Replace the placeholder UUIDs with IDs
returned by Syntra. The reply shortcut prepares a private message addressed to
every other participant of the thread (reply to all) and prints a separate
command for publication. Check who will receive it before publishing.

```sh
syntra threads --workspace WORKSPACE_UUID
syntra thread --workspace WORKSPACE_UUID --thread THREAD_UUID
syntra reply --workspace WORKSPACE_UUID --thread THREAD_UUID --context-file context.md
```

Only threads have titles. A message is its recipients and a nonblank Markdown
body. Add `--message MESSAGE_UUID` to quote a published message in the same
thread. In MCP `prepare`, set `request.content.formatVersion` to 3 and
`request.content.replyToMessageId` when quoting. This requires Syntra 0.1.76 or
later; earlier client versions can read, but cannot prepare or send messages.

To fork, run `syntra fork --workspace W --thread T --message M --title "Follow-up" --request-id R`.
The new thread opens without a notification and seeds at most 11 participants,
including its creator. Repeat `--member` to add active workspace members. Use
repeated `--source-member` flags to select specific source participants, or
`--no-source-members` to select none. Retry with the same arguments and request ID.

To choose a different recipient, generate the reply request with
`syntra prepare --template reply --workspace WORKSPACE_UUID --thread THREAD_UUID`,
fill the placeholders in a private JSON file, then run
`syntra prepare --file request.json`. When building the request yourself, copy
the thread's opaque revision to the top-level `expectedThreadRevision` field.
Use [the reply and retry guide](https://syntra.synapsynclabs.com/docs/guides/replies-and-retry.html)
for the full workflow, including recovery after an uncertain send. To start a new
conversation, see [Find a team and send a thread](https://syntra.synapsynclabs.com/docs/guides/workspaces-to-thread.html).

## Updates and launch modes

| Mode | Version used by a new MCP process |
| --- | --- |
| `latest` (default for new registrations) | npm resolves `@synapsync/syntra-mcp@latest` with `--prefer-online`. It can reuse cached files, including when the registry is unavailable. A cold cache requires registry access. |
| `local` | Starts the npm-global installation you selected during setup, using absolute Node and launcher paths. There is no registry check at MCP startup. |

Existing registrations stay as configured. To migrate a recognized registration,
preview the change, then apply it and confirm in a terminal. Choose `local`
instead if you want to use the global installation running setup. Explicit
noninteractive migration uses `--replace` together with `--mode`.

```sh
syntra setup --agent codex --mode latest --dry-run
syntra setup --agent codex --mode latest
```

`syntra update --check` compares the installed CLI with the npm release and
reports recognized user-level host registrations. `syntra update` updates the
executing npm-global installation after confirmation. It leaves host settings
intact. Restart the host after a change. To verify a running MCP process, read
`serverInfo.version` from its MCP `initialize` response. See
[CLI reference](https://syntra.synapsynclabs.com/docs/reference/cli.html)
for options and exit codes.

## Access and troubleshooting

- **Connection expired.** Run `syntra login --reauthorize`. The default
  connection lifetime is 30 days. On first login you can request up to 90 days
  with `syntra login --lifetime-seconds 7776000`.
- **Missing permission.** Run `syntra login --reauthorize --prepare --publish`
  to request preparation and publication. Use `--prepare` alone for preparation
  only. If you supply permission flags, include every optional permission you
  want to keep. The browser shows exactly what you can approve.
- **CLI works but the agent asks you to log in.** Compare MCP `identity` with
  `syntra status`. Check that both use the same OS user and profile directory.
  Keep credentials out of host configuration and agent prompts.
- **The agent reports an older version.** Open a new host session and check
  whether the host uses a version pin, a local launcher or a project override.

- **The agent still follows the old skill.** `syntra update` leaves installed
  skills in place. Use `syntra setup --agent codex --dry-run` (or your host's
  name) to find the skill directory. To replace a previously installed copy,
  move it to a private backup outside the host's skill directories, run setup
  again, then open a fresh session. Review any custom changes before replacing it.

More in [troubleshooting](https://syntra.synapsynclabs.com/docs/troubleshooting.html).

## Development

Use Go 1.27.3, Python 3 and Node.js 22.12 or later.

```sh
python3 scripts/check-protocol-parity.py
python3 scripts/test-protocol-parity.py
python3 scripts/test-core.py
npm run test:launcher
npm run test:setup
```

See [npm release operations](https://github.com/synapsync/syntra-mcp/blob/main/docs/NPM-RELEASE.md)
for publication and the platform test matrix.

[MIT license](https://github.com/synapsync/syntra-mcp/blob/main/LICENSE) · [Third-party notices](https://github.com/synapsync/syntra-mcp/blob/main/THIRD-PARTY-NOTICES.md)
