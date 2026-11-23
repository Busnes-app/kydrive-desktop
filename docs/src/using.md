# Everyday tasks

## Add or switch accounts

Use **Add account**, enter the KyDrive address, and approve the link in your browser. Accounts on the same server stay separate, including accounts for different organizations. Select an account in **Accounts** before choosing its workspaces, pausing it, opening its folder or unlinking it. Every linked account continues syncing while another is selected.

Your original installation keeps `~/KyDrive` and its existing workspace choices. Added accounts use separate folders under `~/KyDrive Accounts`, named by an opaque account identifier. Use **Open folder** for the selected account. Its `Unsynced` folder keeps edits that never uploaded.

Existing origin-keyed credentials migrate automatically once the server confirms the account identity. The existing link, signing key and certificate pin stay intact. Older servers that supply only a local immutable user ID remain supported; servers with no immutable identity support the original single-account workflow only. Hosted servers must return KyIdentity issuer, subject and organization to qualify tenant-safe desktop sync.

## Everyday work

Open files under the chosen workspace in `~/KyDrive` with your normal applications. Offline work uploads when connectivity returns. macOS scans local changes at startup and every ten minutes; Linux and Windows also scan after filesystem activity. Server changes are polled. Check status before shutting down when you need a change to be durable on the server. Workspaces sync; individually shared files under Shared with me do not.

## Conflicts and deletes

A stale edit becomes a conflict copy alongside the server version. Inspect both versions and merge deliberately. Review delete confirmations rather than assuming a large deletion was intentional.

## Pause and workspace choices

Pause suspends synchronization; it does not upload outstanding edits. Changing workspace selection or losing access can remove synced copies. Edits that never uploaded are preserved under `~/KyDrive/Unsynced/`; inspect that folder after such a change.

## Unlink or revoke

Select the account, then use the client unlink operation or revoke the linked computer from KyDrive. Review unsynced work first. Other accounts keep their credentials, files and synchronization. Files stay unless you explicitly choose removal; re-adding that account preserves retained files and requires choosing its workspaces afresh. Server-side revocation and an optional wipe affect local synced copies; preserve independent work you still own.
