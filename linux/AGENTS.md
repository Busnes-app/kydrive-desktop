## Purpose
The Linux shell: a Qt6/Kirigami tray app (`kydrive-desktop`, app id `com.kysecurity.drive`) that drives `kydrive-sync` and renders its state. Conventions follow `../../kypost-Linux` (CMake layout, hardening flags, KStatusNotifierItem tray, KDBusService single instance, KI18n `i18n()` in QML, Flatpak on org.kde.Platform 6.11).

## Ownership
- `src/CoreClient.*` — JSON-RPC 1.0 client over the core's socket (`Core.<Method>`, one JSON object per line); the only link to the core.
- `src/main.cpp` — app setup, tray menu, notifications for new `warn` activity.
- `qml/` — `Main.qml` (link form, workspaces, held deletes, conflicts, unlink dialog) and `Theme.qml`.
- `packaging/` — desktop file, metainfo, icon (from Busnes.app-site `icons/kydrive.png`), systemd user unit, Flatpak manifest.

## Local Contracts
- No sync logic here: every decision comes from the core's answers.
- The shell never sees the token; linking happens in the core.
- If nothing answers on `$XDG_RUNTIME_DIR/kydrive/sync.sock`, the shell starts `kydrive-sync` once; the systemd unit is the native way to keep it running.
- Colours come from `Theme.qml` (KyPost's Dark/Light Matter values, by system colour scheme); copy palette values, never approximate them.
- Flatpak: `~/KyDrive` only, plus `org.freedesktop.secrets` for go-keyring (it needs the Secret Service itself, not the Secret portal). The Go module is built with network access, so a Flathub build needs vendored modules.

## Work Guidance
- Build in a capped scope (root `AGENTS.md`).

## Verification
- `cmake -B build -S . -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo && cmake --build build && ctest --test-dir build --output-on-failure`. `tst_qml` fails on any QML warning; set `KYDRIVE_SHOT_DIR` to save screenshots.
- CI also builds the Flatpak and validates the desktop file and metainfo.

## Child DOX Index
None.
