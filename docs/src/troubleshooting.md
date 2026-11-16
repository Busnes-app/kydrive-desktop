# Troubleshooting

- **Nothing syncs:** check core status, pause state, workspace selection, network access and server compatibility.
- **Keyring error:** unlock/start the OS keyring service; do not put the token in a configuration file as a workaround.
- **Conflict copy appears:** compare it with the server version before deleting either one.
- **Files vanish after access changes:** check workspace grants and `~/KyDrive/Unsynced/` for edits that had not uploaded.
- **Browser approval has no effect:** confirm that the intended core is running and linked to the same server origin.
- **A large delete waits:** inspect the pending delete confirmation and the local cause before approving it.

When reporting a problem, include the product version, platform, failed task and a redacted error. Exclude pairing links, device secrets, recovery codes, custodian shares and private content.
