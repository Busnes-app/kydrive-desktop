# Security Policy

kydrive-desktop keeps a local copy of a KyDrive workspace in sync. It holds account credentials and file contents on the user's computer and talks to the KyDrive server over HTTPS.

## Reporting vulnerabilities

Report security vulnerabilities privately through GitHub Security Advisories:
<https://github.com/Busnes-app/kydrive-desktop/security/advisories>. Do not open a public issue.
Include the affected area, reproduction steps, impact, and affected versions.
We aim to acknowledge reports within two business days and will coordinate a fix
and disclosure.

## Trust boundaries and limitations

- The device is the trust boundary for the local copy and stored credentials; protect tokens with the OS keystore and never write them to logs.
- Sync must not lose data: conflicts preserve both sides for the user to resolve, and deletions propagate only from an explicit action.
- The client talks only to the configured KyDrive host over HTTPS and refuses redirects to other hosts.

## Operating requirements

- Store credentials in the platform keystore; do not fall back to plaintext on disk.
- Verify the server's TLS certificate; never ship an insecure-skip-verify toggle that is on by default.

## Security-sensitive changes

Changes to credential storage, the sync/conflict engine, deletion propagation, or the server connection require tests for both the expected path and the relevant
failure or attack path, plus a description of the trust boundary in the pull
request.

## Related documents

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution and review requirements
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community expectations
- [README.md](README.md) — what this project is and how to run it

