# Native vault client

## Purpose

Implement the native client portion of KyDrive's vault and CryptPad session protocol.

## Local Contracts

- Use the linked account's signed device and its TLS pin. Accept the witness origin and
  signing key from native setup. Witness requests send opaque receipts and omit credentials.
- Store member secrets only in the OS keyring scoped to the server, vault org and account.
  Keep enrollment retryable with the same key. An existing server identity without a
  matching local key requires recovery.
- Keep plaintext and document-scoped relay grants in the app's private editor host.
  Reverify the witnessed head, live access and signed binding before renewal or export.
  Closure or revocation ends access and preserves unsaved edits for explicit recovery.
- Preserve the DOCX binding shape and recognize the signed XLSX and PPTX format fields.

## Verification

Run `TestRealServerNativeVault` against a disposable server and witness. The
[native vault workflow](../../docs/NATIVE-VAULT.md) gives the required inputs.
