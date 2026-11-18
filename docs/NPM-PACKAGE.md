# Syntra on npm

The package includes the repository's [README](../README.md) from its release
commit. For the build matrix and publishing process, see
[Syntra npm release](./NPM-RELEASE.md).

The published root `package.json` is the version source of truth. The release
builder stages that version in all package manifests and embeds it in each Go
binary with `-ldflags`; the launcher reads its manifest for `--version` and
`version`. Direct development builds default to `0.0.0-handoff`. See the README
for `syntra update`, its confirmation, offline option and exit statuses.
