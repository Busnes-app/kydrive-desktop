#!/bin/bash
# Builds macos/build/KyDrive.app: the universal shell, the universal core and its launch
# agent, ad-hoc signed. Developer ID signing happens in CI's macos-sign job.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
out=$here/build
app=$out/KyDrive.app
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$app/Contents/Library/LaunchAgents"

cd "$here"
swift build -c release --arch arm64 --arch x86_64 --product KyDrive
cp "$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)/KyDrive" "$app/Contents/MacOS/"

for arch in arm64 amd64; do
	(cd ../core && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -o "$out/kydrive-sync-$arch" ./cmd/kydrive-sync)
done
lipo -create "$out/kydrive-sync-arm64" "$out/kydrive-sync-amd64" -output "$app/Contents/MacOS/kydrive-sync"

cp packaging/Info.plist "$app/Contents/"
iconutil -c icns packaging/AppIcon.iconset -o "$app/Contents/Resources/AppIcon.icns" # from scripts/icons.sh
cp packaging/com.kysecurity.drive.sync.plist "$app/Contents/Library/LaunchAgents/"
codesign --force --sign - "$app/Contents/MacOS/kydrive-sync"
codesign --force --sign - "$app"
