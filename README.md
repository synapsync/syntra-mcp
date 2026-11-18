# Syntra CLI and MCP

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

Approve the permissions you need in the browser. The CLI and agent hosts using
the same local profile share that login. To run without a global install, use
`npx -y @synapsync/syntra-mcp login`.

## Connect your agent

Choose one host:

```sh
syntra setup --agent codex
syntra setup --agent claude-code
syntra setup --agent opencode
```

Add `--dry-run` to preview the registration and skill installation. Start a
fresh host session after applying setup, then ask it to list your Syntra
workspaces. For other stdio hosts or a custom profile, use the
[manual configuration templates](https://syntra.synapsynclabs.com/docs/agent-setup/host-configurations.html).

## Permissions and renewal

| Capability | Use |
| --- | --- |
| `READ` | Discover workspaces, projects, teams and members, and read authorized tickets and messages. |
| `PREPARE` | Create private drafts and link them to a member. |
| `PUBLISH` | Send an exact retained draft in a separate call. Requires `PREPARE`. |

A new connection requests 30 days by default. You can request up to 90 days
with `syntra login --lifetime-seconds 7776000`. When a compatible connection
expires, run `syntra login --reauthorize` with the same profile.

If the terminal works and the agent lacks a permission, check the agent's
selected profile with `syntra status`. If the registration specifies
`--profile-dir`, use that same directory in the CLI. See
[troubleshooting](https://syntra.synapsynclabs.com/docs/troubleshooting.html)
for permission changes, profile mismatches and startup errors.

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

See [release operations](docs/NPM-RELEASE.md) for the build matrix and publishing
workflow. The client is [MIT licensed](LICENSE). Compiled dependencies and
bundled components have [third-party notices](THIRD-PARTY-NOTICES.md).
