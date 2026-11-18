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

## Automatic releases

A push to `main` in `synapsync/syntra-mcp` triggers the release workflow
when `NPM_PUBLISH_ENABLED=true`. GitHub reserves the next patch version
with a `vX.Y.Z` tag on the exact commit. The workflow tests the candidate
on all five native targets, publishes through npm Trusted Publishing,
and verifies a fresh install from the public registry. A rerun resumes
the same commit and version. Publication skips packages with matching
integrity and fails if a published version contains different bytes.

The npm package ships the root `README.md` from its release commit.
npm displays the README of the currently published version.

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

## One-time publishing setup

For each of the six existing npm packages, configure a
[trusted publisher](https://docs.npmjs.com/trusted-publishers/) with these
settings and complete npm's one-time confirmation:

- Repository: `synapsync/syntra-mcp`
- Workflow: `npm-release.yml`
- Environment: `npm-production`
- Action: allow direct `npm publish`

Restrict the GitHub environment to the `main` branch. Direct publication
uses GitHub's short-lived OIDC identity without a stored npm token or
an environment approval step. Enable the repository variable
`NPM_PUBLISH_ENABLED=true` after setting up all six packages. Push to
`main` to exercise the connection before npm's first-publication deadline.

## Recover a failed run

Rerun the workflow while the commit is still the tip of `main`.
It reuses the reserved version and verifies each existing package against
the candidate. If the registry already has different bytes under that
version, fix the cause and let a new commit allocate the next version.
Leave the existing tags and published versions intact.
