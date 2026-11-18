# Syntra CLI and MCP

A reply to another active ticket creator can be prepared with `syntra reply
--workspace WORKSPACE_UUID --ticket TICKET_UUID --summary "Summary" --context-file
context.md`. It prints the draft ID and a separate explicit publish command.
If the creator is the caller or cannot be resolved to an active named member, use
the template and select a recipient. Publication is never implicit.

Local request syntax, routing IDs, revisions and content bounds are checked before
loading the profile. Author identity and retained-base authorization are checked
only after the authenticated connection is loaded. Unsafe diagnostic keys are
redacted; unknown safe keys can include descriptor-derived locations and suggestions.

Start with `syntra --help`. To reply, run `syntra prepare --template reply --workspace WORKSPACE_UUID --ticket TICKET_UUID`, save the JSON privately, replace all `TODO: SYNTRA` placeholders, and run `syntra prepare --file request.json`. Publish separately. The ticket revision belongs at request top level beside `content`.

Syntra connects your agent to the workspaces, project teams and tickets you can
access. Read shared work, prepare private drafts and send tickets or replies
when you ask. The client runs locally over stdio and connects to the Syntra API
using a profile you authorize in the browser.

**[Official documentation](https://syntra.synapsynclabs.com/docs/) · [npm package](https://www.npmjs.com/package/@synapsync/syntra-mcp)**

## Get started

Use Node.js 22.12 or later. npm installs the matching Go executable for Linux
x64 or arm64, macOS Intel or Apple Silicon, or Windows x64.

```sh
npm install --global @synapsync/syntra-mcp
syntra login
syntra status
syntra workspaces
```

One login serves every agent session of the same OS user using the default
Syntra profile. The CLI and MCP read the same private profile at each call.
Approve the permissions you need in the browser. To run without a global install,
use `npx -y @synapsync/syntra-mcp login`.

## Connect your agent

Choose one host:

```sh
syntra setup --agent codex
syntra setup --agent claude-code
syntra setup --agent opencode
```

Add `--dry-run` to preview the registration and skill installation. For a
recognized stale Syntra entry, review the before/after preview and confirm the
replacement, or use `--replace` to apply it without prompting. Setup retains
a private config backup and preserves an existing different skill on replacement.
Start a fresh host session after changing its registration, then ask it to list
your Syntra workspaces. For other stdio hosts or a custom profile, use the
[manual configuration templates](https://syntra.synapsynclabs.com/docs/agent-setup/host-configurations.html).

## Permissions and renewal

| Permission | Login flag | Use |
| --- | --- | --- |
| read | Always requested | Discover and read authorized workspaces, people, tickets and messages. |
| prepare | `--prepare` | Create tickets and prepare or link private drafts. |
| publish | `--publish` with `--prepare` | Send an exact retained draft in a separate call. |
| mark-read | `--mark-read` | Permit explicit read receipts in clients that support them. Listing messages does not mark them read. |

A first browser login without permission flags requests read, prepare and publish.
In the browser, you can deselect the optional permissions. If you supply
permission flags, they select which optional permissions to request. An existing
profile is reused by plain login. During reauthorization, include each optional
permission you want to retain when supplying flags. The browser must approve the
new request.

A connection requests 30 days by default. On first login, you can request up to
90 days with `syntra login --lifetime-seconds 7776000`.

| Symptom | Command |
| --- | --- |
| No Syntra connection | `syntra login` |
| Agent lacks prepare or publish | `syntra login --reauthorize --prepare --publish` for both, or omit `--publish` for preparation only. Keep any other permission flags you still need. |
| Connection expired | `syntra login --reauthorize` |
| CLI works but agent keeps asking to log in | Compare `syntra status` with MCP `identity`. Use `syntra setup --agent codex --replace` to move Codex to the shared default, or use the same deliberate `--profile-dir` in both places. |
| Agent still runs an older pinned version | `syntra update`, then follow its `syntra setup --agent … --replace` command. |

## Preparing a reply

Read the ticket with `syntra ticket` and copy its `revision` exactly into
`expectedTicketRevision`. The revision is an opaque base64url token encoding
32 bytes, not a number or a value to invent. Use the ticket's exact title and
project ID (omit `projectId` when absent). Field diagnostics identify invalid or
missing input without including message content.

## Reference

- [CLI commands and flags](https://syntra.synapsynclabs.com/docs/reference/cli.html)
- [MCP tools and effects](https://syntra.synapsynclabs.com/docs/reference/tools.html)
- [Find project teams and send a ticket](https://syntra.synapsynclabs.com/docs/guides/workspaces-to-ticket.html)
- [Replies, retry and recovery](https://syntra.synapsynclabs.com/docs/guides/replies-and-retry.html)

## Build and test

Use Go 1.27.3, Python 3 and Node.js 22.12 or later. The client builds from this
repository with its own Go module.

```sh
python3 scripts/check-protocol-parity.py
python3 scripts/test-protocol-parity.py
python3 scripts/test-core.py
npm run test:launcher
npm run test:setup
```

See [release operations](https://github.com/synapsync/syntra-mcp/blob/main/docs/NPM-RELEASE.md) for the build matrix and publishing
workflow. The client is [MIT licensed](https://github.com/synapsync/syntra-mcp/blob/main/LICENSE). Compiled dependencies and
bundled components have [third-party notices](https://github.com/synapsync/syntra-mcp/blob/main/THIRD-PARTY-NOTICES.md).

## Updates and versions

`syntra --version` and `syntra version` print the installed package version.
Run `syntra update --check` to compare it with the latest stable version from
npm's configured registry. The check exits 0 if current (or newer than the
registry), 10 if an update is available, and 1 on error. `syntra update`
updates a global npm installation after confirmation; `--yes` permits an
unattended update. Set `SYNTRA_UPDATE_OFFLINE=1` to disable network checks.
For npx use `npx -y @synapsync/syntra-mcp@latest ...`; for a checkout or local
install use `npm install -g @synapsync/syntra-mcp@latest` for a global CLI.
Only stable X.Y.Z versions are supported by the updater. Setup pins the exact
package version so the host cannot pick up a new release on startup. After a
global update, the launcher lists any recognized older pins and prints the
command to replace each one. Host config changes use a separate confirmation
or an explicit `--replace`.
