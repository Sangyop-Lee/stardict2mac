#!/bin/sh
# Sign StarDict2Mac with a Developer ID and notarize it, so it opens without
# Gatekeeper warnings. Run on a Mac after packaging/build_app.sh.
#
# Needs a paid Apple Developer Program membership and:
#   DEV_ID       e.g. "Developer ID Application: Your Name (TEAMID)"
#   NOTARY_PROFILE  a keychain profile created once with:
#       xcrun notarytool store-credentials stardict2mac \
#             --apple-id you@example.com --team-id TEAMID
#     (it asks for an app-specific password from appleid.apple.com)
set -eu
cd "$(dirname "$0")/.."
: "${DEV_ID:?set DEV_ID to your Developer ID Application identity}"
NOTARY_PROFILE=${NOTARY_PROFILE:-stardict2mac}
VERSION=$(sed -n 's/^const AppVersion = "\(.*\)"/\1/p' build.go)
APP=dist/StarDict2Mac.app
[ -d "$APP" ] || { echo "Run packaging/build_app.sh first"; exit 1; }

echo "== signing with $DEV_ID (hardened runtime)"
codesign --force --timestamp --options runtime -s "$DEV_ID" "$APP/Contents/MacOS/StarDict2Mac"
codesign --force --timestamp --options runtime -s "$DEV_ID" "$APP"
codesign --verify --strict --verbose=2 "$APP"

echo "== notarizing"
ZIP=dist/StarDict2Mac-notarize.zip
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"
xcrun notarytool submit "$ZIP" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$APP"
spctl --assess --type execute --verbose "$APP"

echo "== packaging"
OUT="dist/StarDict2Mac-$VERSION.zip"
rm -f "$OUT" "$ZIP"
(cd dist && zip -qry "StarDict2Mac-$VERSION.zip" StarDict2Mac.app README.txt LICENSE.txt THIRD_PARTY_NOTICES.txt)
echo "Done: $OUT"
