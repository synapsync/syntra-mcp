# Syntra on npm

The package includes the repository's [README](../README.md) from its release
commit. For the build matrix and publishing process, see
[Syntra npm release](./NPM-RELEASE.md).

The published root `package.json` is the version source of truth. The release
builder stages that version in all package manifests and embeds it in each Go
binary with `-ldflags`; the launcher reads its manifest for `--version` and
`version`. Direct development builds default to `0.0.0-handoff`. See the README
for `syntra update`, its confirmation, offline option and exit statuses.

## Shared profile and exact host pins

A single browser login serves CLI and MCP sessions of the same OS user when they
use the default profile. Set up a supported host once with `syntra setup --agent
codex`, `syntra setup --agent claude-code`, or `syntra setup --agent opencode`.
The registration contains an exact package version. A successful global update
reports older recognized pins and gives a command to refresh each. Review the
setup preview, then confirm interactively or pass `--replace`. A successful
replacement retains the previous config as a private backup.

| Symptom | Command |
| --- | --- |
| Agent and CLI disagree about login or permissions | Compare MCP `identity` with `syntra status`. For Codex, preview the shared-profile registration with `syntra setup --agent codex --dry-run`. |
| No connection | `syntra login`, then choose from the requested permissions in the browser. |
| Missing permission | `syntra login --reauthorize --prepare --publish` for private preparation and explicit sending. Use `--prepare` alone for preparation, or include `--mark-read` for explicit read receipts. |
| Older pinned Codex registration after update | `syntra setup --agent codex --replace`, then start a fresh session. |

For a deliberately isolated profile, use the same custom `--profile-dir` for CLI
and MCP. Setup can identify registrations calling the Syntra executable with
`mcp` or the official package through npx. A registration it cannot identify must
be resolved by its owner.
