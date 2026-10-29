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
- **Strong TLS is the core default:** `core/go.mod` sets `godebug fips140=on` for
  every packaged core; runtime overrides must retain it in the supported profile.
  Shells contain no HTTPS client. docs/TLS-KEY-POLICY.md owns endpoint-key migration and profile limits.
- **The server is the source of truth for wire shapes:** KyDrive-server `internal/api` (`drive_changes.go`, `drive_uploads.go`, `devices.go`).
- Shells contain no sync logic: they call the core and render what it reports.
- Go dependencies are limited to `modernc.org/sqlite`, `github.com/zalando/go-keyring`, `github.com/fsnotify/fsnotify`; any addition needs an MIT/BSD/Apache-compatible licence and an entry in docs/DEPENDENCIES.md.

## Work Guidance
- YAGNI: minimal code, short meaningful comments.
- Write the failing test first and watch it fail.
- Run every Go build or test, and every CMake build, in a capped scope: `systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 -- <cmd>`.

## Verification

- Scorecard (`.github/workflows/scorecard.yml`) publishes public supply-chain security results on default-branch pushes, weekly and after branch-protection changes. Keep publishing restricted to the default branch, action pins immutable, checkout credentials disabled and OIDC/code-scanning writes scoped to the analysis job. README’s live Scorecard badge needs a successful published run.
- `.github/workflows/ci.yml` runs the core tests on Ubuntu, Windows and macOS, builds and tests the Linux shell, builds the Flatpak, builds, tests and launches the macOS app, and signs and notarizes it on `main` and tags once the secrets exist. On a `v*` tag the `release` job checks the tag against the four version fields listed in docs/RELEASING.md and publishes every package, with `SHA256SUMS` and build provenance attestations, to a GitHub Release. A suffixed tag is a pre-release whose notes start with `.github/release-notes/prerelease.md`; keep that file true to what the packages are (it says they are unsigned).
- CodeQL (`.github/workflows/codeql.yml`, `security-extended`) scans Actions and Go on every pull request; Swift, C# and C++ need a build on each OS runner, so they run on push to `main`, weekly and by hand. Repository default setup must stay off or its uploads are rejected.
- `windows-shell` builds and tests the Windows shell against a fake and the real core, and builds the MSIX and zip.
- Every release build of the core (Windows step, `macos/scripts/bundle.sh`, the Flatpak manifest) uses `CGO_ENABLED=0 -trimpath -ldflags=-buildid=`; the `core` job's `release core is repeatable` step builds all five targets from two directories and fails unless they match and embed `DefaultGODEBUG=fips140=on`. Change the flags in all four places together. `docs/VERIFYING-RELEASES.md` tells users how to check a release; `docs/ASSURANCE-CASE.md` must change with any trust boundary.
- Real-server checks are in `core/AGENTS.md`.
- CI's `real-server` job builds an immutable public KyDrive server source pin and exercises
  the complete engine suite and built-daemon smoke test, supplementing the
  three-platform fake/core and native-shell jobs. The smoke test owns a private
  D-Bus/keyring session and disposable server data; it must retain token-custody,
  watcher-upload, revoke/wipe, preserved-edit, relink and unlink assertions.
  Release publication depends on this job too.

## Child DOX Index
- `docs/` — `VERIFYING-RELEASES.md` and `ASSURANCE-CASE.md` (no AGENTS.md; root owns them). `CONTRIBUTING.md` holds the coding standard.
- `core/` — Go sync core: `kydrive-sync` daemon and `ctl`, REST client, linking, sync engine.
- `linux/` — Qt6/Kirigami tray shell, systemd user unit and Flatpak.
- `macos/` — SwiftUI menu bar shell, the core as a bundled launch agent, signing.
- `windows/` — WinUI 3 tray shell, unsigned MSIX and zip.
