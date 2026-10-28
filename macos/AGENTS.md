## Purpose
The macOS shell: a SwiftUI `MenuBarExtra` app (`KyDrive.app`, bundle id `com.kysecurity.drive`, macOS 13+) that drives `kydrive-sync` and renders its state. A Swift package; `scripts/bundle.sh` assembles the app.

## Ownership
- `Sources/KyDriveKit/CoreClient.swift` — socket path, JSON-RPC 1.0 client (`Core.<Method>`, one JSON object per line, one connection per call), the core's reply types; the only link to the core.
- `Sources/KyDriveKit/AppModel.swift` — what the menu shows, actions, warning announcements, `webURL`, `notice`.
- `Sources/KyDriveKit/Views.swift` — the menu window: link form, workspaces, held deletes, conflicts, unlink.
- `Sources/KyDrive/KyDriveApp.swift` — `@main`, launch agent registration, notifications, starting the core without the agent.
- `packaging/` — `Info.plist` (`LSUIElement`) and the launch agent `com.kysecurity.drive.sync.plist`.
- `scripts/bundle.sh` — `build/KyDrive.app`: universal shell and core (`Contents/MacOS/`), agent plist (`Contents/Library/LaunchAgents/`), ad-hoc signed.

## Local Contracts
- No sync logic here: every decision comes from the core's answers. The shell never sees the token; the core keeps it in the Keychain and opens the browser to link.
- **Socket:** `$HOME/Library/Application Support/kydrive/sync.sock`, exactly the core's `filepath.Join(os.UserConfigDir(), "kydrive", "sync.sock")`. A path over 103 bytes (`sun_path`) is refused with a clear error, never truncated.
- **Plain text:** every core or server string is shown with `Text(verbatim:)`. A string literal with interpolation, `LocalizedStringKey(…)` and `AttributedString(markdown:)` parse Markdown; `PlainTextTests` refuses them in any file that imports SwiftUI. Notification bodies are plain `UNNotificationContent.body`.
- **URLs:** only `http`/`https` with a host are opened (`webURL`). The sync root is revealed with `selectFile(_:inFileViewerRootedAtPath:)`, which never launches anything.
- **Core lifecycle:** the app registers the bundled core as a per-user launch agent with `SMAppService.agent` (`RunAtLoad`, restarted after a crash). Only when the agent is not enabled (refused, or switched off in Login Items) and nothing answers does the app start the core itself, once; never both, as two cores would contend for one socket.

## Work Guidance
- Builds and tests need macOS and Xcode; on Linux, push and read the `macos-shell` CI job.
- Sparkle auto-update is not done yet.
- Signing (`macos-sign` job) runs only on pushes to `main` and `v*` tags, only when `APPLE_DEVELOPER_ID_P12` is set (the Linux `signing` job checks; otherwise `macos-sign` is skipped and a release ships the unsigned zip). Repository secrets it needs:
  - `APPLE_DEVELOPER_ID_P12` — base64 of the Developer ID Application certificate and private key (`.p12`).
  - `APPLE_DEVELOPER_ID_P12_PASSWORD` — that `.p12`'s password.
  - `APPLE_TEAM_ID` — the 10-character team ID; the job checks the signature carries it.
  - `APPLE_NOTARY_KEY` — base64 of an App Store Connect API key (`AuthKey_….p8`) with Developer access, for `notarytool`.
  - `APPLE_NOTARY_KEY_ID`, `APPLE_NOTARY_ISSUER_ID` — that key's ID and issuer ID.

## Verification
- `swift test` in `macos/`; set `KYDRIVE_SYNC_BIN` to a built `kydrive-sync` to also check the real core listens on the shell's path with 0700/0600 modes.
- `scripts/bundle.sh`, then open `build/KyDrive.app`.
- CI `macos-shell` runs both, checks the bundle (plists, both architectures, signature), launches the app until its core answers `ctl Status`, and uploads `KyDrive-macos-unsigned.zip`.

## Child DOX Index
None.
