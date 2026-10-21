# DOX framework

- AGENTS.md files are binding work contracts for their subtrees.
- Before editing, read this file and every AGENTS.md on the path to each file you touch; the closer doc controls local detail, and no child doc may weaken a parent.
- After a meaningful change, update the closest owning AGENTS.md (purpose, contracts, workflow, verification, child index) and remove stale text in the same change.
- Section order: Purpose, Ownership, Local Contracts, Work Guidance, Verification, Child DOX Index. Document stable contracts, not history.

## Purpose
KyDrive desktop sync: a linked Windows, macOS or Linux computer keeps chosen KyDrive workspaces in `~/KyDrive`, working offline, syncing both ways. Milestone M7 of KyDrive-server; the plan is `docs/superpowers/plans/2026-10-18-m7-desktop-sync.md` in that repo (Decisions 1-12, Part B).

## Ownership
Root owns the repo layout, CI, licence, dependency list and the app icon: `assets/kydrive-icon.svg` (the busnes.app site icon without its page background) is the only source, and `scripts/icons.sh` renders every platform's icon files from it; never edit those by hand. One Go core per user (`core/`) does all syncing; a thin native shell per OS drives it over local JSON-RPC. One repo so core and shells version together.

## Local Contracts
- **User work is never deleted.** No path deletes a local edit that never uploaded: stale edits become conflict copies uploaded beside the server version; lost access, unchoosing a workspace, a server-side removal and a revoke with wipe move such files to `~/KyDrive/Unsynced/`.
- **Tokens live only in the OS keyring** (service `KyDrive`, user = server origin). Never on disk, in logs or in IPC answers.
- **IPC is owner-only:** a Unix socket at 0600 inside a 0700 directory (Windows: the per-user profile's ACL).
- **The server is the source of truth for wire shapes:** KyDrive-server `internal/api` (`drive_changes.go`, `drive_uploads.go`, `devices.go`).
- Shells contain no sync logic: they call the core and render what it reports.
- Go dependencies are limited to `modernc.org/sqlite`, `github.com/zalando/go-keyring`, `github.com/fsnotify/fsnotify`; any addition needs an MIT/BSD/Apache-compatible licence and a README entry.

## Work Guidance
- YAGNI: minimal code, short meaningful comments.
- Write the failing test first and watch it fail.
- Run every Go build or test, and every CMake build, in a capped scope: `systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 -- <cmd>`.

## Verification
- `.github/workflows/ci.yml` runs the core tests on Ubuntu, Windows and macOS, builds and tests the Linux shell, builds the Flatpak, builds, tests and launches the macOS app, and signs and notarizes it on `main` and tags once the secrets exist. On a `v*` tag the `release` job checks the tag against the four version fields listed in the README and publishes every package to a GitHub Release.
- `windows-shell` builds and tests the Windows shell against a fake and the real core, and builds the MSIX and zip.
- Real-server checks are in `core/AGENTS.md`.

## Child DOX Index
- `core/` — Go sync core: `kydrive-sync` daemon and `ctl`, REST client, linking, sync engine.
- `linux/` — Qt6/Kirigami tray shell, systemd user unit and Flatpak.
- `macos/` — SwiftUI menu bar shell, the core as a bundled launch agent, signing.
- `windows/` — WinUI 3 tray shell, unsigned MSIX and zip.
