# Troubleshooting

- **Nothing syncs:** check core status, pause state, workspace selection, network access and server compatibility.
- **Syncing stopped because the server's certificate changed:** the new certificate was not signed by the server's identity key (the server has no `KYDRIVE_TLS_CERT_FILE`, or something between you and the server presented its own certificate). Ask the operator to check the server's certificate setup, then sign in again: it succeeds only once the server vouches for its certificate. If Sign in again is refused with "Unlink, then link again", the only way back is to unlink this computer and link it again from a network you trust. Your files stay on disk, but you choose its workspaces again, and they sync into new folders beside the old ones.
- **Keyring error:** unlock/start the OS keyring service and wait for automatic recovery. The daemon remains available, but Sign in again waits until the existing credential can be read safely. Restore a missing credential from the OS keyring if possible; otherwise explicitly unlink and link again, keeping local files and choosing workspaces afresh. Do not put the token in a configuration file as a workaround.
- **Conflict copy appears:** compare it with the server version before deleting either one.
- **Files vanish after access changes:** check workspace grants and `~/KyDrive/Unsynced/` for edits that had not uploaded.
- **Browser approval has no effect:** confirm that the intended core is running and linked to the same server origin.
- **A large delete waits:** inspect the pending delete confirmation and the local cause before approving it.
- **A computer link was revoked:** select that account and use its existing Link form. Approve the same identity on the saved server; its certificate must still match the saved pin or a signed successor. The core deletes the revoked token/signing key but keeps a pin-only keyring record. If the keyring is unavailable, unlock it and wait for recovery before retrying. Explicit Unlink removes the pin too and requires choosing workspaces afresh.

## Reporting a problem

When reporting a problem, include the product version, platform, failed task and a redacted error. Exclude pairing links, device secrets, recovery codes, custodian shares and private content.
