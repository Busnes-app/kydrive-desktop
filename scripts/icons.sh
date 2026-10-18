#!/bin/bash
# Renders every app icon from assets/kydrive-icon.svg. Needs rsvg-convert and ImageMagick.
# macOS keeps the SVG's margin (Apple's icon grid); Windows and Linux use the rounded square
# edge to edge, as the busnes.app site does. Run from anywhere; commit what it writes.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
src=$root/assets/kydrive-icon.svg
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The edge-to-edge view: the rounded square's own box.
sed 's/width="1254" height="1254" viewBox="0 0 1254 1254"/width="927" height="928" viewBox="163 162 927 928"/' "$src" >"$tmp/full.svg"
grep -q 'viewBox="163 162 927 928"' "$tmp/full.svg" || { echo "crop failed: the SVG header changed" >&2; exit 1; }

png() { rsvg-convert -w "$2" -h "$2" "$1" -o "$3"; } # svg size out

# macOS: an iconset bundle.sh turns into AppIcon.icns with Apple's iconutil.
set_=$root/macos/packaging/AppIcon.iconset
rm -rf "$set_" && mkdir -p "$set_"
for s in 16 32 128 256 512; do
	png "$src" "$s" "$set_/icon_${s}x${s}.png"
	png "$src" $((s * 2)) "$set_/icon_${s}x${s}@2x.png"
done

# Windows: the manifest's logos, the taskbar's target sizes (plated and unplated) and the .ico.
a=$root/windows/KyDrive/Assets
png "$tmp/full.svg" 44 "$a/logo44.png"
png "$tmp/full.svg" 50 "$a/logo50.png"
png "$tmp/full.svg" 150 "$a/logo150.png"
for s in 16 24 32 48 256; do
	png "$tmp/full.svg" "$s" "$a/logo44.targetsize-$s.png"
	cp "$a/logo44.targetsize-$s.png" "$a/logo44.targetsize-${s}_altform-unplated.png"
done
for s in 16 24 32 48 64 256; do png "$tmp/full.svg" "$s" "$tmp/ico-$s.png"; done
magick "$tmp"/ico-{16,24,32,48,64,256}.png "$a/kydrive.ico"

# Linux: hicolor PNG plus the scalable SVG.
h=$root/linux/packaging/icons/hicolor
png "$tmp/full.svg" 256 "$h/256x256/apps/com.kysecurity.drive.png"
mkdir -p "$h/scalable/apps"
cp "$tmp/full.svg" "$h/scalable/apps/com.kysecurity.drive.svg"
