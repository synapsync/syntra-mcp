# Syntra CLI and MCP

Syntra runs as a local MCP server over standard input and output. It connects to
the hosted Syntra API using an account you authorize in the browser.

The launcher requires Node.js 22.12 or newer. npm installs the matching Go
executable for Linux x64/arm64, macOS Intel/Apple Silicon, or Windows x64.

```sh
npm install --global @synapsync/syntra-mcp
syntra login
syntra status
```

Start the server with `syntra mcp`, or configure a supported host with
`syntra setup --agent codex`, `syntra setup --agent claude-code`, or
`syntra setup --agent opencode`. Add `--dry-run` to preview the host changes.
After applying setup, start a fresh host session.

## Shared login and permissions

Run `syntra login` once on the machine running the MCP and approve the
permissions you need in the browser. The CLI and registrations created by
`syntra setup` use the same default profile. If a manual registration supplies
`--profile-dir`, use that same flag and directory for `syntra login` and
`syntra status`, or update the registration to use your existing login profile.

`syntra status` shows the permissions granted and the expiration. Read lets
agents query Syntra. Prepare lets them save private drafts. Publish is also
required to send. If you already have a profile with the required permissions,
point the MCP at it. To request additional permissions, use `syntra login`
with a separate profile and approve the new grant in your browser.

A new connection requests 30 days by default. For a new connection you can
request up to 90 days with `syntra login --lifetime-seconds 7776000`, subject
to what you approve in the browser. Renew an expired connection with
`syntra login --reauthorize` against the same profile.

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
