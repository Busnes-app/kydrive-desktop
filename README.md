# kydrive-desktop

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

Requires a KyDrive server with milestone M7 (change feed, client mtimes, linked computers).

## Install and link to your server

Install [KyDrive Server](https://github.com/Busnes-app/kydrive-server) first, provision an
everyday account through KyIdentity and grant it workspace access. This client is a sync
companion, not a standalone file server. Your server must implement M7 linked-computer and
change-feed APIs; an older published server image may not include them.

Download the packages from [GitHub Releases](https://github.com/Busnes-app/kydrive-desktop/releases)
(see [Releases](#releases)), or build one with the platform steps below. Linux needs a working Secret
Service keyring; macOS uses Keychain and Windows uses Credential Manager.

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

## Layout

| Path | What |
|---|---|
| `core/` | Go sync core: `kydrive-sync`, one per user, driven over a local socket |
| `linux/` | Qt6/Kirigami tray app (`kydrive-desktop`), systemd user unit, Flatpak |
| `macos/` | SwiftUI menu bar app (`KyDrive.app`), the core as a bundled launch agent |
| `windows/` | WinUI 3 tray app (`KyDrive.exe`), MSIX and zip |
| `scripts/smoke.sh` | Drives a built `kydrive-sync` against a real server |

## Build and run the core

Use Go matching `core/go.mod`; clone the repository and start from its root. On Linux,
macOS and Windows, run only one core per user. A running desktop app may already own it.

```sh
cd core
go build ./cmd/kydrive-sync
./kydrive-sync &                                   # the core
./kydrive-sync ctl Link '{"Server":"https://drive.example.com"}'
./kydrive-sync ctl Workspaces
./kydrive-sync ctl Choose '{"IDs":["<workspace id>"]}'
./kydrive-sync ctl Status
```

`ctl` methods: `Status`, `Link`, `Unlink`, `Workspaces`, `Choose`, `Pause`, `Conflicts`,
`Activity`, `ConfirmDeletes`. They are JSON-RPC 1.0 calls `Core.<Method>` on the socket at
`$XDG_RUNTIME_DIR/kydrive/sync.sock` (Linux), `~/Library/Application Support/kydrive/sync.sock`
(macOS) or `<user config dir>/kydrive/sync.sock`.

## Linux app

From the repository root (build and install both the sync core and shell):

```sh
(cd core && go build -o kydrive-sync ./cmd/kydrive-sync)
cmake -B linux/build -S linux -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  -DCMAKE_INSTALL_PREFIX=/usr/local
cmake --build linux/build
ctest --test-dir linux/build --output-on-failure
sudo install -m 755 core/kydrive-sync /usr/local/bin/kydrive-sync
sudo cmake --install linux/build
# Optional login startup after installing the generated unit:
systemctl --user daemon-reload
systemctl --user enable --now kydrive-sync
# Launch the tray app after installation:
kydrive-desktop
```

Needs CMake 3.21+, Ninja, a C++20 compiler, Qt 6.5+, Kirigami, KI18n, KDBusAddons and KStatusNotifierItem (KF6). The Flatpak
manifest is `linux/packaging/flatpak/com.kysecurity.drive.yml`. Install the released bundle with
`flatpak install --user KyDrive-linux.flatpak`; the KDE runtime comes from Flathub.

## macOS app

```sh
cd macos
swift test
scripts/bundle.sh && open build/KyDrive.app     # needs Xcode and Go
```

Needs macOS 13+. The app registers the bundled core as a login item; until CI signs it with a
Developer ID, the built app is only ad-hoc signed.

## Windows app

CI builds an unsigned MSIX and a zip for x64 and arm64 (artifacts `kydrive-windows-msix-unsigned`
and `kydrive-windows-zip`). Until the MSIX is signed it installs only on Windows 11 (or Server 2025), from an
administrator PowerShell: `Add-AppxPackage -Path .\KyDrive_….msix -AllowUnsigned`. The zip runs
unpacked: start `KyDrive.exe`, which starts `kydrive-sync.exe` beside it. Building needs Windows
and the .NET 10 SDK; see `windows/AGENTS.md`.

## Releases

Pushing a `v*` tag builds every package in CI and, once all jobs pass, publishes them to a GitHub
Release: `KyDrive-linux.flatpak`, the macOS zip (`KyDrive-macos.zip` when Developer ID signed,
otherwise `KyDrive-macos-unsigned.zip`), and the Windows MSIX and zip for x64 and arm64. A tag
with a suffix such as `v0.2.0-rc1` becomes a pre-release. First bump the version in
`linux/CMakeLists.txt`, the newest `<release>` in `linux/packaging/com.kysecurity.drive.metainfo.xml`,
`CFBundleShortVersionString` in `macos/packaging/Info.plist` and `Identity Version` (with a
trailing `.0`) in `windows/KyDrive/Package.appxmanifest`. The release job refuses a tag that
does not match all four.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

## Dependencies

| Module | Licence | Why |
|---|---|---|
| `modernc.org/sqlite` | BSD-3-Clause | CGO-free SQLite for the state database |
| `github.com/zalando/go-keyring` | MIT | OS keyring |
| `github.com/fsnotify/fsnotify` | BSD-3-Clause | file system events |

Indirect: `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory`,
`golang.org/x/sys`, `github.com/google/uuid`, `github.com/remyoudompheng/bigfft`
(BSD-3-Clause); `github.com/godbus/dbus/v5` (BSD-2-Clause); `github.com/danieljoos/wincred`,
`github.com/dustin/go-humanize`, `github.com/mattn/go-isatty`, `github.com/ncruces/go-strftime`
(MIT).

Windows app (NuGet):

| Package | Licence | Why |
|---|---|---|
| `Microsoft.WindowsAppSDK` | Microsoft Software License Terms (redistributable) | WinUI 3, app lifecycle, toasts; bundled in the MSIX and zip |
| `H.NotifyIcon.WinUI` | MIT | tray icon and its menu |

Indirect: `H.NotifyIcon`, `H.GeneratedIcons.System.Drawing`, `System.Drawing.Common` (MIT).
Tests only: `xunit`, `xunit.runner.visualstudio` (Apache-2.0); `Microsoft.NET.Test.Sdk` (MIT).

## Licence

MIT. See `LICENSE`.
