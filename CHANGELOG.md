# Changelog

## 0.7.0 — pending

- Accept complete matching hosted identities without a legacy local user ID, preserving strict issuer/subject/organization checks and account storage through upgrades.
- Keep the Linux Add account form in add mode after approval fails, so retry cannot accidentally relink the selected account.

- Keep the previous sign-in credential intact until a replacement commits, and recover interrupted keyring cleanup without changing account folders or workspace choices.

- Restore selected revoked accounts through their existing Link form, preserving folders/workspaces and enforcing the original identity and certificate pin. Explicit Add account stays separate; revoked tokens/signing keys are replaced by pin-only keyring records.

- Treat legacy server hostname case and default ports as the same account, preserving the original keyring migration source, sync folder and storage selector.

- Finish activation once its credential pointer is committed, even if retiring the previous item fails. Retry pending cleanup without restart and reserve the old slot until cleanup completes.

- Re-add migrated accounts in their original database and sync folder, preserving the storage selector, state and retained files.

- Preserve buffered TLS handshake bytes in the HTTPS proxy test fixture; keep strict pin-refusal and no-request assertions deterministic. Production transport behavior is unchanged.

- Reject duplicate identities across primary and added account linking before changing bindings or credentials; unlinking an inactive primary cannot delete another account’s link.

- Wait for saved credential recovery before signing in again when the OS keyring is unavailable.

- Refresh account workspaces and warning cursors consistently after adding an account or falling back when a selected account disappears.

- Keep older-core upgrades and linking after the last account removal working on Windows; automatically retry account migration when the OS keyring is temporarily unavailable.

- Bump all six version fields to 0.7.0 so each updated shell replaces an older single-account daemon.
- Isolate desktop accounts, credentials and state on shared KyDrive origins; add account selection, browser linking and removal to each shell, with idempotent legacy credential migration.

## 0.6.1 — pending

- TLS pin rollover regression tests replay the retired leaf's earlier signed statement, avoiding a wall-clock boundary flake while retaining pin and no-credential-leak assertions. Runtime TLS behavior is unchanged.

- Keep SQLite `v1.62.0` with its upstream-required libc `v1.80.1` and memory `v1.12.1`; defer independent updates of those runtime dependencies.

- Prepare application version `0.6.1` (from `0.6.0`) for the compatible dependency refresh. Rebuild or install the updated application; this version bump requires no additional data, account, key or configuration migration.
