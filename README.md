# kydrive-desktop

[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15938/badge)](https://www.bestpractices.dev/projects/15938)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Busnes-app/kydrive-desktop/badge)](https://scorecard.dev/viewer/?uri=github.com/Busnes-app/kydrive-desktop)

KyDrive desktop sync client for Windows, macOS and Linux. A linked computer keeps the
workspaces you choose in `~/KyDrive`, works offline, and syncs edits both ways with your
KyDrive server.

- **Conflict handling.** A stale edit becomes a conflict copy beside the server's version.
  Losing access to a workspace, or a revoke that asks for a wipe, removes the synced copies
  but first moves anything that never uploaded to `~/KyDrive/Unsynced/`.
- **Credentials stay in the OS keyring** (Windows Credential Manager, macOS Keychain, the
  Secret Service on Linux), rather than plaintext client configuration. The keyring may persist encrypted data on disk; its protection depends on the OS and keyring setup.
- **Linking happens in your browser:** you sign in to KyDrive as usual and approve the
  computer. Revoke it any time from your KyDrive account page.

Requires KyDrive Server 0.4.3 or later, the first release with linked computers and the change
feed (milestone M7). Workspaces sync; files and folders shared with you individually (Shared with
me) do not.

## Install and link to your server

Install [KyDrive Server](https://github.com/Busnes-app/kydrive-server) first, provision an
everyday account through KyIdentity and grant it workspace access. This client is a sync
companion, not a standalone file server. The server must run KyDrive Server 0.4.3 or later;
older versions lack the linked-computer and change-feed APIs.

Download a package from [GitHub Releases](https://github.com/Busnes-app/kydrive-desktop/releases)
(see [Releases](docs/RELEASING.md#releases)), or build one with the platform steps [below](docs/BUILDING.md#linux-app). Linux needs a working Secret
Service keyring; macOS uses Keychain and Windows uses Credential Manager.

- **Linux:** `flatpak install --user KyDrive-linux.flatpak`; the KDE runtime comes from Flathub.
  The bundle does not update itself: install the next release's bundle the same way.
- **macOS 13+:** unzip `KyDrive-macos.zip`, move `KyDrive.app` to Applications and open it. A
  release that ships `KyDrive-macos-unsigned.zip` instead is not Developer ID signed: macOS
  blocks the first launch until you choose **Open Anyway** in System Settings → Privacy & Security.
- **Windows (x64 or arm64):** extract `KyDrive-windows-<arch>.zip` and start `KyDrive.exe`. It is
  unsigned, so SmartScreen asks first (**More info** → **Run anyway**). The unsigned MSIX installs
  only on Windows 11 or Server 2025, from an administrator PowerShell:
  `Add-AppxPackage -Path .\KyDrive_<version>_x64.msix -AllowUnsigned`.

Check a download against the release's `SHA256SUMS` (`sha256sum -c SHA256SUMS --ignore-missing`;
`shasum -a 256 -c` on macOS) and its build provenance with
`gh attestation verify <file> -R Busnes-app/kydrive-desktop`.
[docs/VERIFYING-RELEASES.md](docs/VERIFYING-RELEASES.md) has the full check, including that the
attestation names the tagged commit, and how to rebuild the core and compare hashes.

1. Start the native app and link it to your own HTTPS drive origin.
2. Complete sign-in in the browser and approve the computer. Choose workspaces to sync.
3. Create a disposable file locally and confirm it appears on the server; edit it on the server and confirm the local update. Test offline/reconnect and a deliberate conflict before relying on synchronization.
4. Review conflicts and `~/KyDrive/Unsynced/`. It preserves pending uploads in the handled revocation/access-loss paths, but it is not an independent backup or protection from disk loss.

Preserve your profile/state, chosen local folders and OS keyring during upgrades. Revoking
a computer or unlinking has deliberate local-copy behavior; review pending edits first.
Files kept when unlinking stay separate: newly selected workspaces use fresh folders, adding
` (2)` or another number when a name is already on disk. Retained files are never automatically
uploaded to the newly linked account.
The real-server script `scripts/smoke.sh` requires an actual compatible test server and
disposable account/data. Unit tests and fake-core shell tests are separate from this gate.
CI runs the full engine suite and the built-daemon smoke test against a pinned public
KyDrive server build in its `real-server` job. The smoke test uses a private D-Bus
session/keyring and disposable data to check linking, file-watcher uploads, token
custody, revocation/wipe, retained local edits and unlinking. Short private fixture
directories avoid Unix setup-socket path limits.

## Licence

MIT. See `LICENSE`.

[![OctoCounts](https://api.octocounts.com/badge/Busnes-app/kydrive-desktop/branch/main)](https://octocounts.com/github/Busnes-app/kydrive-desktop/tree/main)

## Documentation

- [Building](docs/BUILDING.md) — the repository layout, running the sync core, and building the Linux, macOS and Windows apps. <a id="layout"></a><a id="build-and-run-the-core"></a><a id="linux-app"></a><a id="macos-app"></a><a id="windows-app"></a>
- [TLS key policy](docs/TLS-KEY-POLICY.md) — the core's TLS key floors, redirect refusal and what to check before upgrading. <a id="tls-key-policy-and-upgrading"></a>
- [Releasing](docs/RELEASING.md) — what a `v*` tag publishes and the four version fields to bump first. <a id="releases"></a>
- [Dependencies](docs/DEPENDENCIES.md) — every Go and NuGet dependency, its licence and why it is used. <a id="dependencies"></a>
- [Contributing](CONTRIBUTING.md) — how changes land, the test policy and the coding standard.
- [Verifying releases](docs/VERIFYING-RELEASES.md) — check a download's checksum and provenance, and rebuild the core to the same bytes.
- [Assurance case](docs/ASSURANCE-CASE.md) — threat model, trust boundaries, and why the security requirements are met.
