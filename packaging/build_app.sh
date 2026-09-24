#!/bin/sh
# Builds dist/StarDict2Mac.app (universal: Apple Silicon + Intel) and a zip.
# Requirements (on any OS): Go >= 1.22, python3 + Pillow, and rcodesign
# (optional, for ad-hoc signing when not on a Mac).
# Apple's Dictionary Development Kit is not bundled (Apple's terms don't allow
# redistribution); the app sets it up on first use.
set -eu
cd "$(dirname "$0")/.."
ROOT=$(pwd)
RCODESIGN=${RCODESIGN:-rcodesign}
VERSION=$(sed -n 's/^const AppVersion = "\(.*\)"/\1/p' build.go)
DIST=$ROOT/dist
APP=$DIST/StarDict2Mac.app

rm -rf "$DIST"; mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

echo "== compiling $VERSION"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$DIST/sd2m-$arch" .
done
python3 packaging/lipo.py "$APP/Contents/MacOS/StarDict2Mac" "$DIST/sd2m-arm64" "$DIST/sd2m-amd64"
chmod 755 "$APP/Contents/MacOS/StarDict2Mac"
rm "$DIST"/sd2m-*

echo "== icon"
python3 packaging/make_icns.py packaging/icon1024.png "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>en</string>
	<key>CFBundleExecutable</key><string>StarDict2Mac</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundleIdentifier</key><string>io.github.stardict2mac</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>CFBundleName</key><string>StarDict2Mac</string>
	<key>CFBundleDisplayName</key><string>StarDict2Mac</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$VERSION</string>
	<key>CFBundleVersion</key><string>$VERSION</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>LSUIElement</key><true/>
	<key>LSApplicationCategoryType</key><string>public.app-category.utilities</string>
	<key>NSHumanReadableCopyright</key><string>© 2026 Sangyop Lee. MIT License. Apple's Dictionary Development Kit is not included.</string>
	<key>NSAppleEventsUsageDescription</key><string>StarDict2Mac shows file pickers and restarts Dictionary.app after installing a dictionary.</string>
</dict>
</plist>
EOF
printf 'APPL????' > "$APP/Contents/PkgInfo"

echo "== signing (ad hoc)"
if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep -s - "$APP"
elif command -v "$RCODESIGN" >/dev/null 2>&1 || [ -x "$RCODESIGN" ]; then
  "$RCODESIGN" sign "$APP" >/dev/null
else
  echo "   (no signing tool; skipping)"
fi

echo "== zip"
cp packaging/README.txt "$DIST/README.txt"
cp LICENSE "$DIST/LICENSE.txt"
cp THIRD_PARTY_NOTICES.txt "$DIST/THIRD_PARTY_NOTICES.txt"
(cd "$DIST" && zip -qry "StarDict2Mac-$VERSION.zip" StarDict2Mac.app README.txt LICENSE.txt THIRD_PARTY_NOTICES.txt)
ls -la "$DIST"
