# Everyday tasks

## Everyday work

Open files under the chosen workspace in `~/KyDrive` with your normal applications. Offline work uploads when connectivity returns. macOS scans local changes at startup and every ten minutes; Linux and Windows also scan after filesystem activity. Server changes are polled. Check status before shutting down when you need a change to be durable on the server. Workspaces sync; individually shared files under Shared with me do not.

## Conflicts and deletes

A stale edit becomes a conflict copy alongside the server version. Inspect both versions and merge deliberately. Review delete confirmations rather than assuming a large deletion was intentional.

## Pause and workspace choices

Pause suspends synchronization; it does not upload outstanding edits. Changing workspace selection or losing access can remove synced copies. Edits that never uploaded are preserved under `~/KyDrive/Unsynced/`; inspect that folder after such a change.

## Unlink or revoke

Use the client unlink operation or revoke the linked computer from KyDrive. Review unsynced work first. Server-side revocation and an optional wipe affect local synced copies; preserve independent work you still own.
