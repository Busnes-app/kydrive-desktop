# Assurance case

[Overview](../README.md) · [Verifying releases](VERIFYING-RELEASES.md) · [Contributing](../CONTRIBUTING.md)

This is the argument that the KyDrive desktop client meets its security requirements: what it
protects, from whom, where the trust boundaries are, and why the design and the code hold at each
one. The binding contracts are in [AGENTS.md](../AGENTS.md) and the AGENTS.md of each subtree;
this page ties them together and points at the evidence. Paths are relative to the repository
root.

## Security requirements

1. **The device token stays secret.** It lives only in the OS keyring: never on disk in the
   client's own files, in logs, or in answers over the local socket.
2. **Only this user can drive the core.** Other local users cannot call the sync core.
3. **The server cannot write outside the sync folder.** No name or path from the server or the
   state database leads outside the workspace folder, including through a symlink.
4. **Downloaded files are the files the server listed**: same size, same SHA-256.
5. **User work is never silently lost.** A conflict, lost access, revocation or a large delete
   keeps the user's unuploaded edits.
6. **Linking cannot be hijacked**: an approval reaches only the core that started it.
7. **Server and core text cannot become markup or a launched program** in the shells.

## Threat model

| Attacker | Can | Wants |
|---|---|---|
| Malicious or compromised KyDrive server | Send any file names, metadata and content | Write outside `~/KyDrive`, delete local work, run code |
| Network attacker | Read and alter traffic | Steal the token, alter files |
| Another local user | Connect to sockets and read world-readable files | Drive this user's core, read the token |
| Local web page or app during linking | Send requests to the loopback redirect listener | Inject its own authorization code |
| Someone who replaced a release download | Serve a modified package | Get it installed |

Out of scope: malware running as the same user (it can read the keyring and the files anyway),
a compromised OS keyring, and loss of the disk. `Unsynced/` is not a backup (README).

## Trust boundaries

1. **Core → KyDrive server.** HTTPS only, except `http` on a loopback host for development.
   `core/go.mod` enables Go's `fips140=on` verified-chain key policy by default,
   including trusted roots/intermediates. The daemon's shared client refuses redirects;
   trust/hostname verification remains required. See README's TLS migration requirements.
   Every server name and path is checked before it touches disk.
2. **Shell → core**, over a local socket: on Unix a `0600` socket in a `0700` directory; on
   Windows the per-user `%LOCALAPPDATA%` profile's ACL. Shells contain no sync logic.
3. **Browser → loopback listener** during linking: PKCE plus a random `state`.
4. **Core → OS keyring** for the token.
5. **Release workflow → user**: SHA-256 checksums and build provenance attestations
   ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

## Secure design principles applied

- **Fail-safe defaults.** A name the server rule would refuse is skipped and listed, not
  written. A plain 401 stops syncing without deleting anything. A large delete waits for
  confirmation.
- **Least privilege.** The Flatpak gets `~/KyDrive` and the Secret Service, not the home
  directory. CI workflows default to `contents: read`; only the `release` job can write.
- **Economy of mechanism.** One core does all syncing; shells only render what it reports. Three
  direct Go dependencies; SQLite without cgo.
- **Complete mediation.** Every path built from server or database data goes through `Engine.abs`.
- **Separation of privilege.** Linking needs the user's own browser sign-in and approval on the
  server; the core never sees the password.
- **Psychological acceptability.** Conflicts become a visible copy beside the server's version
  rather than a choice the user must make at once.

## Common weaknesses and how they are countered

| Weakness | Counter | Where |
|---|---|---|
| Path traversal from server names (CWE-22) | `serverName` refuses empty, `.`/`..`, separators, control and format characters; `Engine.abs` requires `filepath.IsLocal` and refuses backslashes | `core/engine/push.go` (`serverName`), `core/engine/engine.go` (`abs`), `TestServerNamesStayInside`, `TestNameHazards` |
| Symlink following out of the workspace (CWE-59) | `abs` refuses any ancestor that is a symlink (`os.Lstat`) | `core/engine/engine.go`, `TestSymlinkedParentRefused` |
| Corrupted or substituted download | Size and SHA-256 must match the server's digest; a file with no digest is refused; content goes to a temp file and is renamed in only if the target is still free or unchanged | `core/api/api.go` (`Download`), `core/engine/pull.go`, `TestDownloadChecksDigest`, `TestDownloadRejectsWrongDigest` |
| Credential exposure (CWE-522) | Token stored with `keyring.Set` and read with `keyring.Get`; `Status` answers carry `Linked`, not the token; unlink and revoke delete the keyring item | `core/cmd/kydrive-sync/rpc.go`, `core/cmd/kydrive-sync/daemon.go`, `TestTokenOnlyInKeyring`, `TestUnlinkDeletesKeyringItem` |
| Weak TLS certificate keys | Go runtime policy refuses RSA below 2048 bits and unsupported EC certificate keys throughout the verified chain; strong controls retain access | `core/go.mod`, `TestDaemonRejectsUndersizedTLSKeys` |
| Redirect to another origin or cleartext endpoint | Shared daemon HTTP client refuses redirects before forwarding tokens/verifiers; direct loopback HTTP remains development-only | `daemon.go`, `TestDaemonRefusesRedirects` |
| Cleartext transport (CWE-319) | `serverOrigin` accepts `https`, or `http` only for `localhost`/loopback | `core/cmd/kydrive-sync/rpc.go` (`serverOrigin`), `TestLinkRefusesPlainHTTP` |
| Authorization-code interception during linking | Loopback listener on `127.0.0.1` with a random port; PKCE S256 challenge; `state` compared in constant time; one code accepted; 10-minute limit | `core/link/link.go`, `TestLink`, `TestLinkTimesOut` |
| Local privilege boundary (CWE-276) | Unix socket `0600` in a `0700` directory; a second core refuses to replace a live socket; on Windows CI proves another local user cannot call the core | `core/cmd/kydrive-sync/daemon.go` (`listen`), `TestSocketIsOwnerOnly`, `TestSecondDaemonRefused`, `windows-shell` step "Socket ACL and another user" |
| Data loss from server-driven deletes | Unuploaded edits move to `Unsynced/`; a push deleting more than half a workspace and over 20 items, or whose folder is gone, waits for `ConfirmDeletes` | `core/engine/pull.go` (`remove`), `core/engine/push.go`, `TestLostAccessKeepsUnsynced`, `TestWipeKeepsUnsynced`, `TestLargeDeletePauses` |
| Markup or URL injection in the shells (CWE-79-like) | macOS shows core strings with `Text(verbatim:)` and opens only `http`/`https` URLs; Windows uses plain string properties, builds toasts as XML trees and launches only `http`/`https` | `macos/Tests/KyDriveKitTests/PlainTextTests.swift`, `macos/Sources/KyDriveKit/AppModel.swift` (`webURL`), `windows/KyDrive.Client/Safe.cs`, `windows/KyDrive.Tests/SafetyTests.cs` |
| Unbounded error bodies | Error responses read through a 4 KiB limit; downloads read at most the listed size plus one byte | `core/api/api.go` |
| Vulnerable dependencies | `go mod verify` in CI; three direct Go modules, each listed with its licence | `.github/workflows/ci.yml` (`core`), `README.md` "Dependencies" |
| Tampered release | SHA-256 checksums and build provenance attestations; the core rebuilds to the same bytes | [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md), `release core is repeatable` step |

## Residual risk

- **The packages are not code-signed.** Windows and macOS warn on first launch, and a user who
  skips the checks on [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md) has nothing else to tell a
  genuine package from a substitute.
- **The core is built with the Go named in `core/go.mod`'s `go` line**, currently 1.26.10; CI runs pinned `govulncheck`. Toolchain updates remain necessary for
  future standard-library fixes.
- **The Linux core uses the SDK extension's Go**, subject to the module minimum/toolchain selection; the bundled binary must still carry the strong TLS default.
- **The keyring may persist the token on disk**, encrypted as the OS and keyring decide (README).
- **The large-delete guard is a threshold**, not a guarantee: smaller deletes go through.
- **No static analysis beyond `go vet`** runs in CI (no CodeQL or similar), and there are no fuzz
  targets.
- **Runtime overrides can weaken compiled defaults.** The supported profile retains
  `fips140=on` and normal TLS settings. A launch environment that turns it off is not
  evidence of the strong profile; the external system browser has its own TLS policy.
- No independent security audit has been done.

## Evidence

- **Tests:** the `core` job runs `go test -race` on Linux and macOS and `go test` on Windows. The
  `real-server` job builds an immutable public KyDrive server source pin and runs the full
  engine race suite against it (`core/AGENTS.md`). It also builds the actual daemon
  and runs `scripts/smoke.sh` with disposable server data and a private D-Bus/keyring
  session, checking token custody, watcher uploads, revoke/wipe and retained edits,
  relinking and unlinking. Publication waits for that job too.
  Each shell has unit tests against a
  fake core; the macOS and Windows tests also run against the real core, and CI launches the
  built macOS app, Windows zip and MSIX until each reaches its core.
- **TLS:** 40 real daemon-client key/operation cases cover weak RSA leaf/root/intermediate
  and P-224 root refusal, strong RSA/P-256/Ed25519 acceptance, with separate redirect and
  normal trust/hostname controls. Requests include link redemption and file transfers;
  a failed TLS operation retains the credential, workspace binding and local edit.
- **Static analysis:** `go vet` and `gofmt` on the core; compiler warnings on the Linux shell;
  `tst_qml` fails on QML warnings.
- **Releases:** checksums, attestations and a repeatable core ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

A pull request that changes a trust boundary or a security requirement must update this page in
the same change.
