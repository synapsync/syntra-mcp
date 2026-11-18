# Syntra CLI and MCP

Syntra runs as a local MCP server over standard input and output. It connects to
the hosted Syntra API using an account you authorize in the browser.

After the first npm release, install from the registry. The launcher
requires Node.js 22.12 or newer. npm installs the matching Go executable for
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

## Build from source

Use Go 1.27.3, Python 3, and Node.js 22.12 or newer. The standalone Go
client, launcher, and protocol tests run without a Syntra backend checkout.

```sh
python3 scripts/check-protocol-parity.py
python3 scripts/test-protocol-parity.py
python3 scripts/test-core.py
npm run test:launcher
npm run test:setup
```

See [release operations](docs/NPM-RELEASE.md) for packaging and CI.
