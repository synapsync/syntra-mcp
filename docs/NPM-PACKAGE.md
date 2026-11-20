# Syntra on npm

The package includes the repository's [README](../README.md) from its release
commit. For the build matrix and publishing process, see
[Syntra npm release](./NPM-RELEASE.md).

The published root `package.json` is the version source of truth. The release
builder stages that version in all package manifests and embeds it in each Go
binary with `-ldflags`; the launcher reads its manifest for `--version` and
`version`, and the MCP process reports its embedded version in `initialize`. Direct development builds default to `0.0.0-handoff`. See the README
for the update workflow and the [CLI reference](https://syntra.synapsynclabs.com/docs/reference/cli.html) for flags and exit statuses.

## Shared profile and launch modes

A single browser login serves CLI and MCP sessions of the same OS user when they
use the default profile. Set up a supported host once with `syntra setup --agent
codex`, `syntra setup --agent claude-code`, or `syntra setup --agent opencode`.
New entries use `latest`, resolving the npm tag at each MCP start with
`--prefer-online`. To avoid startup registry resolution, run setup from the
selected npm-global installation with `--mode local`. This uses absolute paths
to Node and its installed launcher on Linux, macOS, and Windows. Windows uses
Node plus npm's npx CLI entry point for `latest`.

Existing registrations remain unchanged unless you choose a mode explicitly
and confirm the migration. Apply with a terminal confirmation or pass
`--replace` together with a mode. Setup keeps the previous config as a private
backup and preserves the MCP arguments and other host settings. Update checks
report the recognized configured mode even when the executing package is
current. Running sessions and host config precedence require host inspection.

| Symptom | Command |
| --- | --- |
| Agent and CLI disagree about login or permissions | Compare MCP `identity` with `syntra status`. Make sure the host and CLI use the same OS user and profile directory. Setup migration preserves existing MCP arguments, including an explicit profile path. |
| No connection | `syntra login`, then choose from the requested permissions in the browser. |
| Missing permission | `syntra login --reauthorize --prepare --publish` for private preparation and explicit sending. Use `--prepare` alone for preparation only. Agents never mark threads read: a thread is read when a person opens it in the Syntra web app. |
| Older pinned Codex registration after update | To opt out of the pin, preview with `syntra setup --agent codex --mode latest --dry-run`, then apply and start a fresh session. |

For a deliberately isolated profile, use the same custom `--profile-dir` for CLI
and MCP. The global config locations inspected are Codex's `config.toml`
under `CODEX_HOME` or `~/.codex`, Claude Code's `.claude.json` under
`CLAUDE_CONFIG_DIR` or the home directory, and OpenCode's global
`opencode.json` or `opencode.jsonc` under the XDG config root. Existing
OpenCode V1 entries under `mcp.syntra` retain their layout. Setup reports
unsupported custom launchers or ambiguous global configs for manual review.
