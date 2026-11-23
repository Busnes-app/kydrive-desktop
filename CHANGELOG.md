# Changelog

## 0.7.0 — pending

- Wait for saved credential recovery before signing in again when the OS keyring is unavailable.

- Refresh account workspaces and warning cursors consistently after adding an account or falling back when a selected account disappears.

- Keep older-core upgrades and linking after the last account removal working on Windows; automatically retry account migration when the OS keyring is temporarily unavailable.

- Bump all six version fields to 0.7.0 so each updated shell replaces an older single-account daemon.
- Isolate desktop accounts, credentials and state on shared KyDrive origins; add account selection, browser linking and removal to each shell, with idempotent legacy credential migration.

## 0.6.1 — pending

- TLS pin rollover regression tests replay the retired leaf's earlier signed statement, avoiding a wall-clock boundary flake while retaining pin and no-credential-leak assertions. Runtime TLS behavior is unchanged.

- Keep SQLite `v1.62.0` with its upstream-required libc `v1.80.1` and memory `v1.12.1`; defer independent updates of those runtime dependencies.

- Prepare application version `0.6.1` (from `0.6.0`) for the compatible dependency refresh. Rebuild or install the updated application; this version bump requires no additional data, account, key or configuration migration.
