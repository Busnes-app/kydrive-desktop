# Assurance case

[Overview](../README.md) · [Verifying releases](VERIFYING-RELEASES.md) · [Contributing](../CONTRIBUTING.md)

This is the argument that the KyDrive desktop client meets its security requirements: what it
protects, from whom, where the trust boundaries are, and why the design and the code hold at each
one. The binding contracts are in [AGENTS.md](../AGENTS.md) and the AGENTS.md of each subtree;
this page ties them together and points at the evidence. Paths are relative to the repository
root.

## Security requirements

1. **The device token stays secret.** It lives only in the OS keyring: never on disk in the
   client's own files, in logs, or in answers over the local socket. The device's signing key
   is held the same way, in the same keyring item, and the token alone is refused without it.
   It is sent only to the TLS leaf this computer pinned, or a successor the server signed.
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
   trust/hostname verification remains required. See the [TLS migration requirements](TLS-KEY-POLICY.md#tls-key-policy-and-upgrading).
   Past that, the core pins the leaf key and repins only to one the server's Ed25519 identity
   key signed ([TLS leaf pinning](TLS-KEY-POLICY.md#tls-leaf-pinning)).
   Every server name and path is checked before it touches disk.
2. **Shell → core**, over a local socket: on Unix a `0600` socket in a `0700` directory; on
   Windows the per-user `%LOCALAPPDATA%` profile's ACL. Shells contain no sync logic.
3. **Browser → loopback listener** during linking: PKCE plus a random `state`.
4. **Core → OS keyring** for each account’s token, signing key and TLS pin. Account entries use a SHA-256 hash of the server origin, KyIdentity issuer, subject and optional organization; old local-user IDs use a separate namespace. Migration copies the complete credential before committing an atomic database pointer/cleanup marker, then deletes the old entry. Revocation replaces the item with a token-field `-` record containing no token/signing key and retaining only the TLS pin (or known legacy absence); restart remains revoked. Restoring the selected account uses the same owner, server and pinned pre-browser checks as SignIn. Explicit Unlink removes that record. `Accounts` and scoped RPCs expose no credentials. Each account owns its database, root, watcher and retry memory; opaque request selectors prevent a UI switch from redirecting another account’s operation. The original installation keeps its paths; new roots are siblings under `~/KyDrive Accounts`. The manager checks identity ownership before resetting bindings or publishing any Link/AddAccount/SignIn approval, using the same reservation through the browser wait and commit. Unlinked metadata owns no credential slot. Tests in `accounts_test.go` cover shared-origin isolation, migration/retry, removal and restart; `duplicate_link_test.go` covers both concurrent approval orders, duplicate primary rejection, unchanged credential bytes/state/files, continued sync and old-server primary links.
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
| Credential exposure (CWE-522) | Token and signing key stored together in one keyring item (`credential.go`); `Status` answers carry `Linked`, not the token; unlink and revoke delete the keyring item | `core/cmd/kydrive-sync/rpc.go`, `core/cmd/kydrive-sync/daemon.go`, `TestTokenOnlyInKeyring`, `TestUnlinkDeletesKeyringItem` |
| Stolen bearer token replayed elsewhere | Linking enrols a P-256 key; every request carries an ECDSA signature over origin, method, request URI, date, a random nonce, the server-acted headers and the body's SHA-256; a token whose device enrolled a key is refused unsigned (kydrive-server `devicesig.go`) | `core/api/sign.go`, `core/link/link.go`, `TestSigningGoldenVector`, `TestSignedRequests`, smoke "the token alone is refused" |
| Interceptor holding a certificate the computer trusts (corporate or malicious CA) | Leaf SPKI pinned at link and checked in `VerifyConnection`, before any request byte; a renewal needs a statement signed by the identity key learned over the pinned leaf, naming the observed leaf and host, within 24 h, newer than the pin's floor; one credential-free fetch, compare-and-swap save, one retry; otherwise syncing stops. Sign in again waits until the saved credential has been read after keyring recovery, then obeys the same rule before the browser opens and redeems over that leaf, or is refused (Unlink and Link again) | `core/api/tlspin.go`, `core/cmd/kydrive-sync/daemon.go` (`sessionPins`), `core/cmd/kydrive-sync/rpc.go` (`SignIn`, `keepPin`), `TestTLSPinGoldenVector`, `TestPinWrongLeafSendsNothing`, `TestPinRolloverRefusals`, `TestPinStatementOlderThanFirstPinRefused`, `TestDaemonUnlinkDuringRolloverNotUndone`, `TestSignInOverUnvouchedLeafRefused`, `TestSignInWaitsForMigrationPinRecovery`, `TestSignInWaitsForUnreadCredential`, `TestRealServerTLSPinRollover` |
| Weak TLS certificate keys | Go runtime policy refuses RSA below 2048 bits and unsupported EC certificate keys throughout the verified chain; strong controls retain access | `core/go.mod`, `TestDaemonRejectsUndersizedTLSKeys` |
| Redirect to another origin or cleartext endpoint | Shared daemon HTTP client refuses redirects before forwarding tokens/verifiers; direct loopback HTTP remains development-only | `daemon.go`, `TestDaemonRefusesRedirects` |
| Cleartext transport (CWE-319) | `serverOrigin` accepts `https`, or `http` only for `localhost`/loopback | `core/cmd/kydrive-sync/rpc.go` (`serverOrigin`), `TestLinkRefusesPlainHTTP` |
| Authorization-code interception during linking | Loopback listener on `127.0.0.1` with a random port; PKCE S256 challenge; `state` compared in constant time; one code accepted; 10-minute limit, or `CancelLink`; `SignIn` refuses an account other than the one recorded at link | `core/link/link.go`, `core/cmd/kydrive-sync/rpc.go`, `TestLink`, `TestLinkTimesOut`, `TestCancelLink`, `TestSignInRefusesAnotherAccount` |
| Reused or renamed account name during Sign in again | Compare immutable `user_id` once recorded; refuse missing/different IDs and revoke the refused credential. Learn existing links through the authenticated device-account route. Username fallback exists only where no ID was recorded | `rpc.go` (`sameAccount`), `engine.go` (`sync`), `TestSignInUsesImmutableAccountID`, `TestLegacyLinkLearnsItsAccountBeforeSignedOut` |
| Concurrent server changes beneath a locally deleted folder | Send the observed workspace `head`; a 409 preserves state and pulls again next pass. Never retry a conflict without the condition; legacy fallback requires a missing account route and retains the feed check | `api.go` (`TrashFolder`), `push.go` (`deletes`), `TestFolderTrashRefusesAChangeAfterItsFeedCheck`, `TestFolderTrashOnlyFallsBackOnOlderServer` |
| Local privilege boundary (CWE-276) | Unix socket `0600` in a `0700` directory; a second core refuses to replace a live socket; on Windows CI proves another local user cannot call the core | `core/cmd/kydrive-sync/daemon.go` (`listen`), `TestSocketIsOwnerOnly`, `TestSecondDaemonRefused`, `windows-shell` step "Socket ACL and another user" |
| Data loss from server-driven deletes | Unuploaded edits move to `Unsynced/`; a push deleting more than half a workspace and over 20 items, or whose folder is gone, waits for `ConfirmDeletes` | `core/engine/pull.go` (`remove`), `core/engine/push.go`, `TestLostAccessKeepsUnsynced`, `TestWipeKeepsUnsynced`, `TestLargeDeletePauses` |
| Markup or URL injection in the shells (CWE-79-like) | macOS shows core strings with `Text(verbatim:)` and opens only `http`/`https` URLs; Windows uses plain string properties, builds toasts as XML trees and launches only `http`/`https` | `macos/Tests/KyDriveKitTests/PlainTextTests.swift`, `macos/Sources/KyDriveKit/AppModel.swift` (`webURL`), `windows/KyDrive.Client/Safe.cs`, `windows/KyDrive.Tests/SafetyTests.cs` |
| Unbounded error bodies | Error responses read through a 4 KiB limit; downloads read at most the listed size plus one byte | `core/api/api.go` |
| Vulnerable dependencies | `go mod verify` in CI; three direct Go modules, each listed with its licence | `.github/workflows/ci.yml` (`core`), `docs/DEPENDENCIES.md` |
| Tampered release | SHA-256 checksums and build provenance attestations; the core rebuilds to the same bytes | [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md), `release core is repeatable` step |

## Residual risk

- **The packages are not code-signed.** Windows and macOS warn on first launch, and a user who
  skips the checks on [VERIFYING-RELEASES.md](VERIFYING-RELEASES.md) has nothing else to tell a
  genuine package from a substitute.
- **The core is built with the Go named in `core/go.mod`'s `go` line**, currently 1.26.11; CI runs pinned `govulncheck`. Toolchain updates remain necessary for
  future standard-library fixes.
- **The Linux core uses the SDK extension's Go**, subject to the module minimum/toolchain selection; the bundled binary must still carry the strong TLS default.
- **The keyring may persist the token on disk**, encrypted as the OS and keyring decide (README).
- **The signing key is a software key in the keyring item beside the token**, not a
  non-exportable TPM or Secure Enclave key, so anything that can read that item can sign. It
  stops a token read off the wire or from a server-side leak, not a reader of the keyring.
- **A computer linked before signing sends its token alone** until it is linked again; the
  server accepts that only while it still allows keyless devices.
- **Pinning trusts the first leaf.** Linking (including after Unlink), and the first sync or
  Sign in again of a computer linked before pinning, accept whatever leaf system trust
  accepts at that moment. Sign in again on a pinned computer does not: it needs the pinned
  leaf or a signed successor. A server without an identity key, or one that cannot sign its
  certificate (503), therefore makes every renewal an Unlink and Link again, which drops the
  workspace bindings (files stay). An older core cannot read a keyring item that holds a pin:
  downgrading needs a sign-in.
- **The large-delete guard is a threshold**, not a guarantee: smaller deletes go through.
- **Older-server compatibility is weaker.** Without a recorded immutable ID, Sign in again compares the saved username; a pre-0.7 link with neither identity remains refused. On a server missing the account route, a folder delete uses the existing feed check, which cannot close the last request window. Upgrade the server and let a valid linked client sync before relying on the new identity and atomic-delete protections.
- **CodeQL scans the Swift, C# and C++ shells only after merge** (push to `main`, weekly), so a
  finding there can land before it is reported. There are no fuzz targets.
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
  Pinning tests use httptest TLS servers with a test-only CA (rollover, each refusal, the
  freshness floor, concurrency, flapping, HTTPS proxies, unlink and re-link during the fetch,
  sign-in again over the stored, a signed renewed and an unvouched leaf, link after unlink),
  and `TestRealServerTLSPinRollover` renews behind a TLS front against the pinned real server.
- **Static analysis:** `go vet` and `gofmt` on the core; compiler warnings on the Linux shell;
  `tst_qml` fails on QML warnings. CodeQL (`security-extended`) scans Go and the workflows on
  every pull request, and the three shells on `main` and weekly.
- **Releases:** checksums, attestations and a repeatable core ([VERIFYING-RELEASES.md](VERIFYING-RELEASES.md)).

A pull request that changes a trust boundary or a security requirement must update this page in
the same change.
