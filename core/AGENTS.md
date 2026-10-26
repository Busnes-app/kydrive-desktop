## Purpose
The sync core, module `github.com/Busnes-app/kydrive-desktop/core`. `kydrive-sync` runs one per user; `kydrive-sync ctl METHOD [JSON]` calls it for debugging.

## Ownership
- `api/` — REST client for the KyDrive server; no sync decisions.
- `link/` — loopback + PKCE linking.
- `engine/` — state DB (`db.go`), pull (`pull.go`), push (`push.go`), conflict/evict rules (`engine.go`).
- `cmd/kydrive-sync/` — daemon: keyring, socket, JSON-RPC service `Core`, watcher, poll loop.

## Local Contracts
- **Pass:** `Engine.Sync` lists workspaces, then per workspace pulls (feed from the cursor; full snapshot on first sync, an unknown folder, or 410) and pushes (scan, moves by inode/file ID, new folders, settled uploads with `expected` revision and `mtime`, then deletes). `Engine.Poll` pulls only. The daemon polls every 45 s and on wake or address change; scans at start, every 10 minutes and after fsnotify activity.
- **Downloads** go to `.kydrive-*.tmp` beside the target, are checked against size and server `digest`, get the server mtime, and are renamed in only if the target is still free or still the unchanged synced copy. A failed one is kept in `retries` and tried again each pull.
- **Paths stay inside the workspace folder.** Every server name passes `serverName` before it becomes a path (a refused one is skipped under a `.kydrive-refused-<id>` placeholder that never touches disk), and `Engine.abs` refuses a non-local path or one with a symlinked ancestor. Use `abs` for every path built from DB or server data.
- **Changed** means size, mtime or inode/file ID differs from what the last sync recorded, or the item is `stale`.
- **Conflict:** local change meets a newer server revision, or the server refuses an upload (409): the local file becomes `<name> (conflict from <device> <YYYY-MM-DD>).<ext>`, uploads as a new file, and is listed as an issue. Identical content is adopted instead.
- **Evict** (lost access = workspace gone from the list or 403/404 on feed/snapshot; unchoose; server removal; wipe): synced unchanged copies are deleted, everything else moves to `Unsynced/<workspace folder>/`.
- **Revoke:** `401 device_revoked` stops syncing, deletes the keyring item, and wipes only when `wipe` is true. A plain 401 stops without deleting. Unlink calls `DELETE /api/devices/self` (best effort) then deletes the keyring item.
- **Large-delete guard:** a push that would delete more than half of a workspace and more than 20 items, or whose workspace folder is missing, waits for `ConfirmDeletes`; refusing re-snapshots and restores the files.
- **Skipped (listed, never synced):** ignored names (`~$*`, `.~lock.*`, `*.tmp`, `.DS_Store`, `Thumbs.db`, `desktop.ini`, both directions so ignoring never reads as a delete); case-only duplicates on Windows/macOS (first by feed order, snapshot sorted by name); Windows-invalid names on Windows; names the server refuses.
- New workspace folders use names absent from both current bindings and the sync root, including retained files/directories from an unlinked account; collisions get ` (2)` and existing paths are never implicitly adopted. `Unsynced` is reserved. Reader workspaces only pull.
- TLS policy is the `core/go.mod` default `godebug fips140=on`, shared across the
  daemon's one HTTP client for linking, APIs and file transfers. Preserve normal
  chain/hostname verification and reject all redirects. Never weaken the runtime
  policy to repair an endpoint: correct its certificate chain. TLS failure must
  preserve credentials, bindings and local work. README owns operator migration.
- Linking accepts `https` servers, or `http` on loopback for development. Every approved link clears previous workspace bindings, cursors and item/retry records under the work lock before saving and activating new credentials; files remain untouched and workspaces must be selected afresh. A refused approval preserves existing bindings. Platform names are `windows`, `macos`, `linux`.

## Work Guidance
- Engine tests use the `remote` interface: the in-process fake (`fake_test.go`) by default, the real server (`e2e_test.go`) when `KYDRIVE_SERVER_BIN` is set. A new engine test goes through `setup` so it runs on both; use `setupOn(t, newFake(t), …)` only for fake-only controls (corrupt bytes, pruned feed).
- Keep the fake's behaviour matched to the server code, not to the plan.
- Real-server fixtures use short, private `kyd-` temporary roots under the caller's
  normal TMPDIR; test-name paths can exceed the server's Unix setup-socket limit.

## Verification
- `TestDaemonRejectsUndersizedTLSKeys` covers real workspace/redemption/download/upload/link
  requests for weak leaf, intermediate and explicit-root keys, plus strong RSA/P-256/Ed25519
  controls. Redirect refusal, untrusted/wrong-host certificates and preservation of
  credentials/bindings/local edits after TLS failure are separate regressions.
- `go vet ./... && go test -race ./...` (and `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...`).
- Against a real server binary built from KyDrive-server: `KYDRIVE_SERVER_BIN=/path/kydrive-server go test -count=1 ./engine/`.
  CI's `real-server` job builds the public commit pinned in `.github/workflows/ci.yml`
  and runs the full engine suite with the race detector; keep fake-only controls separate.
- The built daemon: `scripts/smoke.sh /path/kydrive-server /path/kydrive-sync` (private D-Bus session and gnome-keyring; needs `curl`, `jq`, `secret-tool`, `dbus-run-session`, `gnome-keyring-daemon`).

## Child DOX Index
None.
