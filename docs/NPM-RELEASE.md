# Syntra npm release

The package consists of a JavaScript launcher and five precompiled Go
executables. The launcher pins the exact same version of each optional
platform package. The client can be built without access to the Syntra backend
source.

## Published release

Version `0.1.1` was published through npm Staged Packages after the artifacts
passed [installation tests on all five platforms](https://github.com/synapsync/syntra-mcp/actions/runs/44306641423).
All six registry integrities matched that run's manifest SHA-256
`2f601377df2268d9528e19d9e6635eb38ab0b22965ccd06316727156b35aa48f`.
A fresh Linux x64 install from npm verified automatic native package
selection and MCP startup. The [npm package page](https://www.npmjs.com/package/@synapsync/syntra-mcp)
shows the current release.

## Build a candidate

Use Go 1.27.3, Python 3, and Node.js 22.12 or newer. Download the dependencies
pinned by `core/go.mod` and `core/go.sum` first, then generate the artifacts with
an empty output directory.

```sh
(cd core && go mod download)
python3 scripts/build-npm-packages.py \
  --go-cache "$(go env GOCACHE)" --module-cache "$(go env GOMODCACHE)" \
  --release-version 0.1.0 --repository synapsync/syntra-mcp \
  --output-dir dist/npm
python3 scripts/capture-npm-candidate.py "$PWD/dist/npm" "$PWD/dist/npm-manifest.json" --release
```

Use the GitHub Actions workflow to test the candidate on Linux x64 and arm64,
macOS Intel and Apple Silicon, and Windows x64. A manually dispatched workflow
with a `release_version` input builds and tests the artifacts for review.
The build captures each tarball digest and each packed file digest. Review
those artifacts before publishing. Each package includes MIT and the
third-party notices.

## Bootstrap and subsequent releases

An npm maintainer with permission to publish under `@synapsync` can bootstrap
the six package names with interactive 2FA, after approving the version and
artifact contents. The existing tool expects the six tarballs, their manifest,
and the verified manifest SHA-256.

```sh
python3 scripts/publish-npm-packages.py "$PWD/dist/npm" "$PWD/dist/npm-manifest.json" "$reviewed_manifest_sha256"
```

It publishes the five native packages first, verifies registry integrity, then
publishes the launcher. Test the exact version from the registry after
publication.

For subsequent versions, configure [npm Trusted Publishing](https://docs.npmjs.com/trusted-publishers/)
for each package. Use `synapsync/syntra-mcp`, workflow `npm-release.yml`, and
the `npm-production` environment. Protect the environment and version tags.
Once approved for use, set the `NPM_PUBLISH_ENABLED` repository variable to
`true`. A pushed `vX.Y.Z` tag builds and tests before publishing. The final job
installs the exact version from npm into a fresh consumer.
