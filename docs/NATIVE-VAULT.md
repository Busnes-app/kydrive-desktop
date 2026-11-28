# Native vault integration

Desktop is the first release client under Q13 of the
[whole-drive decisions](https://github.com/Busnes-app/kydrive-server/blob/master/docs/superpowers/specs/2026-11-28-whole-drive-e2e-decisions.md).
A signed app carries the vault core and CryptPad assets. The KyDrive server serves ciphertext,
metadata and grants. The app uses the independently pinned witness and connects to its
configured CryptPad gateway for encrypted room traffic.

## Native core

A signed desktop device discovers the vault account scope with its existing certificate pin
and request signer. The vault core enrolls and restores its member identity from the OS keyring.
It verifies invitations, membership, checkpoints and signed revisions, wraps membership and
editor capabilities locally, and publishes encrypted exports. It checks the exact binding on
renewal and save. Existing DOCX capabilities are accepted. XLSX and PPTX include the format
inside the signed encrypted binding.

The owner-only local daemon accepts `kydrive-sync ctl Vault` for the `identity`, `create`,
and `verify` actions. Each request takes `AccountID`, `Action`, and a `Witness` object
with `WitnessOrigin` and `WitnessKey`. Verification also takes the independently checked
`Invite` returned by vault creation. The result contains public member pins, invitation and
generation. This is a development entry point for disposable servers.

## Editor host and release gates

The native core's editor methods receive capabilities prepared by a bundled CryptPad editor,
return a verified source revision and a short-lived scoped grant, renew that grant, and
encrypt exported bytes. On failed renewal the host closes the editor and preserves valid
unsaved changes for explicit recovery. Use an app-owned WebView with executable assets
from the signed package. Block arbitrary navigation, remote scripts and unscoped network
connections. Authenticate the frame and each message before passing a document or grant.
The desktop edit window, CryptPad asset packaging and native live co-editing proof are open.

Q7's automatic export interval is chosen after an identical workload at 1, 2 and 5 minutes.
Record peak RAM, CPU, bandwidth, retained ciphertext, save latency and restore lag for the
client, gateway and server. Q10 requires client conversion with a formatting-loss warning
for ODT/ODS/ODP/RTF/CSV and local PDF viewing. Hosted recovery uses the recovery key and
member re-add. The Key Steward is Enterprise-only. Q14 starts with an empty catalogue.
The conversion, recovery and full-drive catalogue are open.

## Verification

Build disposable KyDrive server and witness binaries from the native vault server contract
commit pinned in `core/go.mod`. Run from the desktop root:

```sh
cd core
KYDRIVE_SERVER_BIN=/absolute/path/to/kydrive-server \
KYDRIVE_VAULT_WITNESS_BIN=/absolute/path/to/vault-witness \
systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 -- \
  go test -race -count=1 -v ./engine -run '^TestRealServerNativeVault$'
```

The test links two signed desktop devices on one disposable account, uses a real witness,
and simulates the OS keyring. It checks ciphertext on the wire, reopens the binding from
the second device, and verifies its signed encrypted export. Revoking the second device
refuses its renewal and saves while the first device can still read the export.
