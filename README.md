# kydrive-desktop

KyDrive desktop sync client for Windows, macOS and Linux. A linked computer keeps the
workspaces you choose in `~/KyDrive`, works offline, and syncs edits both ways with your
KyDrive server.

- **Edits are never lost.** A stale edit becomes a conflict copy beside the server's version.
  Losing access to a workspace, or a revoke that asks for a wipe, removes the synced copies
  but first moves anything that never uploaded to `~/KyDrive/Unsynced/`.
- **Credentials stay in the OS keyring** (Windows Credential Manager, macOS Keychain, the
  Secret Service on Linux), never on disk.
- **Linking happens in your browser:** you sign in to KyDrive as usual and approve the
  computer. Revoke it any time from your KyDrive account page.

Requires a KyDrive server with milestone M7 (change feed, client mtimes, linked computers).

## Layout

| Path | What |
|---|---|
| `core/` | Go sync core: `kydrive-sync`, one per user, driven over a local socket |
| `linux/` | Qt6/Kirigami tray app (`kydrive-desktop`), systemd user unit, Flatpak |
| `macos/` | SwiftUI menu bar app (`KyDrive.app`), the core as a bundled launch agent |
| `scripts/smoke.sh` | Drives a built `kydrive-sync` against a real server |

## Build and run the core

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

```sh
cd linux
cmake -B build -S . -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo
cmake --build build && ctest --test-dir build
sudo cmake --install build                      # with kydrive-sync in the same bin dir
systemctl --user enable --now kydrive-sync      # optional: run the core at login
```

Needs Qt 6.5+, Kirigami, KI18n, KDBusAddons and KStatusNotifierItem (KF6). The Flatpak
manifest is `linux/packaging/flatpak/com.kysecurity.drive.yml`.

## macOS app

```sh
cd macos
swift test
scripts/bundle.sh && open build/KyDrive.app     # needs Xcode and Go
```

Needs macOS 13+. The app registers the bundled core as a login item; until CI signs it with a
Developer ID, the built app is only ad-hoc signed.

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

## Licence

MIT. See `LICENSE`.
