# Third-party dependencies

[Product overview](../README.md)

Commands and unlinked file paths below are relative to the repository root.

## Dependencies

| Module | Licence | Why |
|---|---|---|
| `modernc.org/sqlite` | BSD-3-Clause | CGO-free SQLite for the state database |
| `github.com/zalando/go-keyring` | MIT | OS keyring |
| `github.com/fsnotify/fsnotify` | BSD-3-Clause | file system events |

Indirect: `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory`,
`golang.org/x/sys`, `github.com/google/uuid`, `github.com/remyoudompheng/bigfft`
(BSD-3-Clause); `github.com/godbus/dbus/v5` (BSD-2-Clause); `github.com/danieljoos/wincred`,
`github.com/dustin/go-humanize`, `github.com/mattn/go-isatty`, `github.com/ncruces/go-strftime`
(MIT).

Windows app (NuGet):

| Package | Licence | Why |
|---|---|---|
| `Microsoft.WindowsAppSDK` | Microsoft Software License Terms (redistributable) | WinUI 3, app lifecycle, toasts; bundled in the MSIX and zip |
| `H.NotifyIcon.WinUI` | MIT | tray icon and its menu |

Indirect: `H.NotifyIcon`, `H.GeneratedIcons.System.Drawing`, `System.Drawing.Common` (MIT).
Tests only: `xunit`, `xunit.runner.visualstudio` (Apache-2.0); `Microsoft.NET.Test.Sdk` (MIT).
