# Building kydrive-desktop

[Product overview](../README.md)

Commands and unlinked file paths below are relative to the repository root.

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
