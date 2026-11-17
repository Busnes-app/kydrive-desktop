# Changelog

## 0.6.1 — pending

- TLS pin rollover regression tests replay the retired leaf's earlier signed statement, avoiding a wall-clock boundary flake while retaining pin and no-credential-leak assertions. Runtime TLS behavior is unchanged.

- Keep SQLite `v1.62.0` with its upstream-required libc `v1.80.1` and memory `v1.12.1`; defer independent updates of those runtime dependencies.

- Prepare application version `0.6.1` (from `0.6.0`) for the compatible dependency refresh. Rebuild or install the updated application; this version bump requires no additional data, account, key or configuration migration.
