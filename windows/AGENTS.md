## Purpose
The Windows shell: a WinUI 3 (Windows App SDK) tray app (`KyDrive.exe`, package `com.kysecurity.drive`) that drives `kydrive-sync.exe` and renders its state. It ships as an MSIX and as an unpackaged zip, both self-contained (.NET and the Windows App SDK bundled).

## Ownership
- `KyDrive.Client/` — `CoreClient` (JSON-RPC 1.0 over the core's AF_UNIX socket, one connection per call), `Paths.SocketPath`, `Safe` (http/https check, toast XML), `Warnings`. Plain `net10.0`, no UI: everything testable lives here.
- `KyDrive/` — `Program.cs` (single instance via `AppInstance`), `App.xaml(.cs)` (resources, toast registration), `MainWindow.cs` (window built in code, tray icon), `Package.appxmanifest`, `Assets/` (from `linux/packaging/icons`).
- `launch-check.ps1` — CI: launches a build and waits, via UI Automation, for the link form.
- `KyDrive.Tests/` — xUnit: IPC against `FakeCore` on a real AF_UNIX socket, the socket path against the real core, link and toast safety, a plain-text guard over the app sources.

## Local Contracts
- No sync logic here: every decision comes from the core's answers. The shell never sees the token.
- **Socket path:** `%APPDATA%\kydrive\sync.sock`, read from the `APPDATA` variable exactly as Go's `os.UserConfigDir` does (not the known-folder API), refusing a relative value. `PathTests` pins it against the real `kydrive-sync.exe`.
- **MSIX AppData write virtualization stays disabled** (`desktop6:FileSystemWriteVirtualization`, `unvirtualizedResources`). Under it the core's `bind()` lands in the package's `LocalCache\Roaming` and the shell's `connect()` to the contract path fails (seen on CI: "An invalid argument was supplied"); it would also give the MSIX and the zip separate state over one `~/KyDrive`.
- **Server and core strings are plain text:** `TextBlock.Text`, `InfoBar.Message` and other string properties only; no `RichTextBlock`, `XamlReader`, `Hyperlink`, `Inlines` or WebView (`SafetyTests.AppRendersPlainTextOnly`). Toasts are built as an XML tree by `Safe.ToastXml`, never by string concatenation or `AppNotificationBuilder`.
- **Links:** only `http`/`https` URIs from `Safe.WebUri` reach `Launcher`. Open folder starts `%WINDIR%\explorer.exe` by full path with an existing absolute directory, never `UseShellExecute`.
- **The core** is `kydrive-sync.exe` beside `KyDrive.exe`. If nothing answers, the app starts it once as a hidden per-user process; it keeps running after the app quits. The csproj fails without `windows/build/core/<Platform>/kydrive-sync.exe`.
- **Tray:** `H.NotifyIcon.WinUI` (MIT), native popup menu (`ContextMenuMode.PopupMenu`); its commands may arrive off the UI thread, so `Command` enqueues on the window's dispatcher.

## Work Guidance
- Building needs Windows; on Linux only `dotnet test KyDrive.Tests` runs (no SDK on the dev machine: use CI). Run any local build in a capped scope (root `AGENTS.md`).
- Build the core first, in `core/`: `GOOS=windows GOARCH=amd64 go build -trimpath -o ../windows/build/core/x64/kydrive-sync.exe ./cmd/kydrive-sync` (`arm64` into `ARM64/`).
- **TODO: signing.** The MSIX is unsigned: `Publisher` carries the unsigned-package OID, and it installs only on Windows 11 (or Server 2025, as on CI) from an administrator PowerShell with `Add-AppxPackage -AllowUnsigned`. A signing job needs a code-signing identity (Azure Trusted Signing: account, certificate profile and a federated OIDC credential for the workflow; or a PFX in a repository secret with its password), `Publisher` set to that certificate's subject without the OID, then `signtool sign /fd SHA256 /tr <timestamp URL>` on the `.msix`. Run it only on tags or `main`, never on `pull_request`. App Installer auto-update comes after signing.

## Verification
- CI job `windows-shell` (`windows-latest`): builds the core for x64 and arm64; `dotnet test KyDrive.Tests` with `KYDRIVE_SYNC_EXE` set; checks the socket directory's ACL and that another local user cannot call the core; builds the MSIX and zip for both architectures and checks both hold `kydrive-sync.exe`; launches the zip build, then installs the MSIX with `-AllowUnsigned` and launches it; each must start its bundled core on the contract path and show the link form (`launch-check.ps1`), and `ctl Status` from outside the package must reach the packaged core. Screenshots are uploaded.
- Artifacts: `kydrive-windows-msix-unsigned`, `kydrive-windows-zip`, `windows-launch-screenshot`.

## Child DOX Index
None.
