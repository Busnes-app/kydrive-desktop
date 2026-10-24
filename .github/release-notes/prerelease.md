> [!WARNING]
> **Test build, unsigned.** This pre-release is for testing on real machines before the first
> stable release. No package is code-signed, so every platform warns before the first launch.
> Sync only disposable files with it. It needs KyDrive Server 0.4.3 or later.

## Install

**macOS 13 or later**: `KyDrive-macos-unsigned.zip` (universal)

1. Unzip it and move `KyDrive.app` to Applications.
2. Gatekeeper blocks the first launch because the app is not Developer ID signed:
   - macOS 13 and 14: Control-click (or right-click) `KyDrive.app`, choose **Open**, then **Open** again.
   - macOS 15 and later: open it once and dismiss the warning. Then go to System Settings →
     Privacy & Security and choose **Open Anyway**.
3. If macOS asks, allow KyDrive in System Settings → General → Login Items. It runs its sync core
   as a background item.

**Windows 10 or 11 (x64 or arm64)**: `KyDrive-windows-x64.zip` / `KyDrive-windows-arm64.zip`

1. Extract the zip and start `KyDrive.exe`.
2. SmartScreen warns because the app is unsigned: choose **More info**, then **Run anyway**.

**Windows 11 (MSIX)**: `KyDrive_<version>_x64.msix` / `KyDrive_<version>_arm64.msix`

The MSIX is an unsigned package, so there is no certificate to trust. Sideload it from an
administrator PowerShell:

```powershell
Add-AppxPackage -Path .\KyDrive_<version>_x64.msix -AllowUnsigned
```

Windows 10 cannot install unsigned packages; use the zip there.

**Linux**: `KyDrive-linux.flatpak`

```sh
flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
flatpak install --user KyDrive-linux.flatpak
flatpak run com.kysecurity.drive
```

It needs a running Secret Service keyring, such as GNOME Keyring or KWallet. The bundle does not
update itself; to update, install the next bundle the same way.

## Check a download

```sh
sha256sum -c SHA256SUMS --ignore-missing      # macOS: shasum -a 256 -c SHA256SUMS
gh attestation verify <file> -R Busnes-app/kydrive-desktop
```
