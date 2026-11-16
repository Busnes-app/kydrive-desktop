# Getting started

## Install and start

Use a published platform package or the source-build instructions in the product reference. Check the package against its release's SHA256SUMS and build provenance. The native shell and the Go sync core must both be available. Linux needs the Secret Service; macOS uses Keychain and Windows uses Credential Manager. The token and software signing key are kept in the OS keyring, whose at-rest protection depends on the OS setup. KyDrive Server 0.4.3 or later is required.

## Link a computer

Start the app/core and enter your server's HTTPS origin. Sign in to KyDrive in the browser and approve the computer. Return to the client, list available workspaces and choose the ones to sync.

Wait for initial sync to finish before making large edits. Create a small file, verify it in the web app, change it there and check that the local copy updates. This establishes both directions for this installation.
