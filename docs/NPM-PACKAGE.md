# Syntra CLI and MCP

Syntra runs as a local MCP server over standard input and output. It connects to
the hosted Syntra API using an account you authorize in the browser.

Requires Node.js 22.12 or newer. npm installs the matching Go executable for
Linux x64/arm64, macOS Intel/Apple Silicon, or Windows x64.

```sh
npm install --global @synapsync/syntra-mcp
syntra login
syntra status
```

Start the server with `syntra mcp`, or configure a supported host with
`syntra setup --agent codex`, `syntra setup --agent claude-code`, or
`syntra setup --agent opencode`. Add `--dry-run` to preview the host changes.
After applying setup, start a fresh host session.

See the included license and third-party notices for distribution terms.
