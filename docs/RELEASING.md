# Releasing

[Product overview](../README.md)

Commands and unlinked file paths below are relative to the repository root.

## Releases

Pushing a `v*` tag builds every package in CI and, once all jobs pass, publishes them to a GitHub
Release: `KyDrive-linux.flatpak`, the macOS zip (`KyDrive-macos.zip` when Developer ID signed,
otherwise `KyDrive-macos-unsigned.zip`), the Windows MSIX and zip for x64 and arm64, and
`SHA256SUMS`, each with a GitHub build provenance attestation. Asset names carry no version, so
`https://github.com/Busnes-app/kydrive-desktop/releases/latest/download/<name>` always points at
the newest release. A tag with a suffix such as `v0.4.0-test.1` becomes a pre-release, a test
build: it is checked against the version without its suffix, and its notes open with
`.github/release-notes/prerelease.md` (the unsigned-build warning and install steps). First bump
the version in `linux/CMakeLists.txt`, the newest `<release>` (version and date) in
`linux/packaging/com.kysecurity.drive.metainfo.xml`,
`CFBundleShortVersionString` in `macos/packaging/Info.plist` and `Identity Version` (with a
trailing `.0`) in `windows/KyDrive/Package.appxmanifest`. The release job refuses a tag that
does not match all four.

```sh
git tag v0.4.0-test.1 && git push origin v0.4.0-test.1
```
