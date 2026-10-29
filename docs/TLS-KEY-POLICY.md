# TLS key policy

[Product overview](../README.md)

Commands and unlinked file paths below are relative to the repository root.

## TLS key policy and upgrading

The core is built with `godebug fips140=on` from `core/go.mod`. This supported
configuration uses Go's built-in TLS policy on Windows, macOS and Linux: the whole
verified certificate chain, including explicitly trusted roots and intermediates,
must use RSA-2048+ or P-256/P-384/P-521 or fixed-size Ed25519 keys. Trust and hostname
verification still apply. It is a runtime policy, not FIPS certification.

The same client handles browser-link code redemption, workspace APIs, uploads and
downloads. It refuses redirects, including HTTPS-to-HTTP downgrades. Configure the
final HTTPS origin directly; a reverse proxy must serve the API there without
redirecting it. Loopback HTTP remains available for development and for RFC 8252's
local browser callback, not for a remote production server.

Before updating, check the server and private-CA chain and replace any undersized
keys. Keep the OS keyring item, state database and `~/KyDrive` files: this update
changes neither tokens nor local data. A TLS refusal pauses the operation with an
error; correct the server certificate chain and retry. Do not unlink or remove
files as a TLS repair.

Go's `GODEBUG` environment overrides compiled defaults. The supported configuration
retains `fips140=on`; if a launcher already sets `GODEBUG`, preserve its other
settings and set its `fips140` entry to `on`, then restart the core. Do not use
`fips140=off`, `fips140=only` or TLS compatibility overrides. Install/update the
core together with its native shell so an older already-running daemon is not
mistaken for the updated one. The browser sign-in page uses your external browser's
TLS settings; native shells send no HTTPS requests themselves.

Build with the Go toolchain selected by `core/go.mod` (currently 1.26.10) or a
newer supported Go release. `go version -m /path/to/kydrive-sync` must show
`DefaultGODEBUG=fips140=on`; release CI checks that setting on every core target.
TLS tests exercise real daemon requests with weak/strong certificate chains and
normal verification, rather than inferring strength from cipher names.

Primary reference: [Go runtime policy](https://go.dev/doc/security/fips140).
