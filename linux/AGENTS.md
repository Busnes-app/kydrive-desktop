## Purpose
The Linux shell: a Qt6/Kirigami tray app (`kydrive-desktop`, app id `com.kysecurity.drive`) that drives `kydrive-sync` and renders its state. Conventions follow `../../kypost-Linux` (CMake layout, hardening flags, KStatusNotifierItem tray, KDBusService single instance, KI18n `i18n()` in QML, Flatpak on org.kde.Platform 6.11).

## Ownership
- `src/CoreClient.*` — JSON-RPC 1.0 client over the core's socket (`Core.<Method>`, one JSON object per line); the only link to the core.
- `src/Appearance.*` — the suite's six theme pairs (ky-ui `THEME_PAIRS` and `tokens.css`), the saved pair and System/Light/Dark mode, migration of a saved suite theme.
- `src/main.cpp` — app setup, tray menu, notifications for new `warn` activity.
- `qml/` — `Main.qml` (brand row, link form, workspaces, held deletes, conflicts, unlink dialog), `AppearanceDialog.qml` (theme pair and light/dark, from the Appearance action), `Theme.qml` (colours, ky-ui type scale and radii), `Banner.qml` (the one status message, as on macOS) and `KyButton.qml` (primary, secondary, danger).
- `packaging/` — desktop file, metainfo, icon (from Busnes.app-site `icons/kydrive.png`), systemd user unit, Flatpak manifest.

## Local Contracts
- No sync logic here: every decision comes from the core's answers.
- The shell never sees the token; linking happens in the core.
- If nothing answers on `$XDG_RUNTIME_DIR/kydrive/sync.sock`, the shell starts `kydrive-sync` once; the systemd unit is the native way to keep it running.
- Colours come from `Theme.qml`, fed by `Appearance`: Busnes following the system by default. Copy palette and status values from ky-ui, never approximate them; `tst_appearance` ports ky-ui's `check-status-colours.mjs` gate (180 checks) and must pass.
- Danger, warning and success never reuse the accent and never stand alone: each has a word and a Kirigami message icon or a dot.
- Look: content is one column capped at 720px; groups are flat cards (panel, 1px line, no shadow); one filled primary button per group, destructive actions in danger; never accent text on accent-soft.
- Settings keys: `appearance/pair` and `appearance/mode` in the app's QSettings. A legacy `theme` value migrates once: Busnes Light/Dark keep that half forced; anything removed becomes Busnes following the system.
- Flatpak: `~/KyDrive` only, plus `org.freedesktop.secrets` for go-keyring (it needs the Secret Service itself, not the Secret portal). The Go module is built with network access, so a Flathub build needs vendored modules.

## Work Guidance
- Build in a capped scope (root `AGENTS.md`).

## Verification
- `cmake -B build -S . -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo && cmake --build build && ctest --test-dir build --output-on-failure`. `tst_qml` fails on any QML warning and renders every theme half; set `KYDRIVE_SHOT_DIR` to save 1440x900 screenshots.
- CI also builds the Flatpak, validates the desktop file and metainfo, and uploads the single-file bundle `KyDrive-linux.flatpak` (artifact `kydrive-linux-flatpak`).

## Child DOX Index
None.
