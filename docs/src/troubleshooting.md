# Troubleshooting

- **Nothing syncs:** check core status, pause state, workspace selection, network access and server compatibility.
- **Asked to sign in again after the server's certificate changed:** the new certificate was not signed by the server's identity key (the server has no `KYDRIVE_TLS_CERT_FILE`, or something between you and the server presented its own certificate). Ask the operator to check the server's certificate setup, then sign in again from a network you trust.
- **Keyring error:** unlock/start the OS keyring service; do not put the token in a configuration file as a workaround.
- **Conflict copy appears:** compare it with the server version before deleting either one.
- **Files vanish after access changes:** check workspace grants and `~/KyDrive/Unsynced/` for edits that had not uploaded.
- **Browser approval has no effect:** confirm that the intended core is running and linked to the same server origin.
- **A large delete waits:** inspect the pending delete confirmation and the local cause before approving it.

When reporting a problem, include the product version, platform, failed task and a redacted error. Exclude pairing links, device secrets, recovery codes, custodian shares and private content.
