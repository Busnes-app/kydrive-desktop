# Verifying releases

[Overview](../README.md) · [Assurance case](ASSURANCE-CASE.md) · [Contributing](../CONTRIBUTING.md)

> **Untested against a real release.** No release has been published yet: the first
> pre-release, `v0.4.0-test.1`, did not finish (its `release` job failed and the tag was removed).
> The steps below follow the `release` job in `.github/workflows/ci.yml`. The rebuild in
> [Rebuild the core](#rebuild-the-core-and-compare) was checked against CI artifacts of `main`,
> not a release. Run this whole page against the first release and fix whatever it gets wrong.

A release is a GitHub Release made by the `release` job when a `v*` tag is pushed. It holds six
packages and a checksum file. Asset names carry no version.

| Asset | What |
|---|---|
| `KyDrive-linux.flatpak` | Single-file Flatpak bundle (shell and core) |
| `KyDrive-macos.zip` or `KyDrive-macos-unsigned.zip` | Universal `KyDrive.app`; the second when Developer ID signing is not set up |
| `KyDrive-windows-x64.zip`, `KyDrive-windows-arm64.zip` | Unpackaged app with `kydrive-sync.exe` beside `KyDrive.exe` |
| `KyDrive_<version>_x64.msix`, `KyDrive_<version>_arm64.msix` | Unsigned MSIX packages |
| `SHA256SUMS` | SHA-256 of the six files above |

## What is signed, and by whom

- **Build provenance.** The `release` job attests every asset, including `SHA256SUMS`, with
  `actions/attest-build-provenance`. That is a SLSA provenance statement signed through GitHub's
  Sigstore integration. There is no long-lived key; the signature is tied to the workflow that
  ran, and that identity is what you check:
  `https://github.com/Busnes-app/kydrive-desktop/.github/workflows/ci.yml@refs/tags/v<version>`.
  `gh` fetches Sigstore's trust root itself.
- **No code signing yet.** The Windows packages are unsigned. The macOS app is only ad-hoc signed
  unless the `macos-sign` job's secrets exist; as of 2026-10-23 they do not, so a release would
  ship `KyDrive-macos-unsigned.zip`. The Flatpak bundle is not GPG-signed. The provenance
  attestation is the only signature on these files.
- There is no SBOM. The Go dependencies are listed in [README.md](../README.md#dependencies), and
  `go version -m` prints the exact module versions inside a core binary.

## Verify a download

You need `gh` (logged in), `sha256sum` (`shasum -a 256` on macOS) and `jq`. Name the version and
file yourself:

```bash
v=<release version without the v, e.g. 0.4.0>
f=<asset name, e.g. KyDrive-windows-x64.zip>
gh release download "v$v" -R Busnes-app/kydrive-desktop -p SHA256SUMS -p "$f" \
  && sha256sum -c SHA256SUMS --ignore-missing \
  && sha=$(gh api "repos/Busnes-app/kydrive-desktop/commits/refs/tags/v$v" --jq .sha) \
  && id="https://github.com/Busnes-app/kydrive-desktop/.github/workflows/ci.yml@refs/tags/v$v" \
  && gh attestation verify "$f" --repo Busnes-app/kydrive-desktop --cert-identity "$id" \
  && [ "$(gh attestation verify "$f" --repo Busnes-app/kydrive-desktop --cert-identity "$id" \
       --format json --jq '.[0].verificationResult.statement.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit')" = "$sha" ] \
  && echo "OK: $f is release v$v, built by ci.yml from $sha"
```

Any failure means: do not install that file. A file built by a different workflow, from a branch
instead of the tag, or in another repository fails the `--cert-identity` or `--repo` check.

## Rebuild the core and compare

The core, `kydrive-sync`, is built without cgo, with `-trimpath` and an empty build ID, so the
same commit and Go version give the same bytes on any machine and in any directory. The `core`
CI job checks this on every push for all five targets. **This holds only for releases built after
this page was added.** Earlier builds kept a host-dependent Go build ID, and the Windows x64 core
was built with cgo enabled.

The Windows zips are the simplest to compare, because their core is not signed:

```bash
v=<release version>  arch=<amd64 or arm64>  zip=KyDrive-windows-<x64 or arm64>.zip
unzip -p "$zip" kydrive-sync.exe > from-release.exe
go_version=$(go version from-release.exe | awk '{print $2}')
git clone --branch "v$v" https://github.com/Busnes-app/kydrive-desktop.git "kydrive-desktop-v$v"
(cd "kydrive-desktop-v$v/core" && GOTOOLCHAIN=$go_version CGO_ENABLED=0 GOOS=windows GOARCH=$arch \
  go build -trimpath -ldflags=-buildid= -o ../../from-source.exe ./cmd/kydrive-sync)
sha256sum from-release.exe from-source.exe
```

The two hashes must match. Build from a fresh clone, not from a git worktree inside another
checkout: Go only recognises a `.git` directory, so in a nested worktree it stamps the outer
repository's commit into the binary and the hashes differ. `go version -m from-release.exe`
shows the commit, Go version and module versions the release was built with.

Not covered:

- **macOS.** `bundle.sh` ad-hoc signs the core after building it, and Developer ID signing would
  re-sign it, so the shipped slices are not the bytes `go build` writes. This page gives no
  comparison for them.
- **Linux.** The Flatpak builds the core with the Go of the `org.freedesktop.Sdk.Extension.golang`
  extension, whose version is not pinned. Reading the core out of the bundle needs
  `flatpak install` or `ostree`. This was not tried.
- **The shells, MSIX, zips and Flatpak bundle as a whole** are not repeatable: archive timestamps,
  .NET and Swift build output, and code signatures differ between builds.
