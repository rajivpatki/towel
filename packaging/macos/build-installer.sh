#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
VERSION="${1:-0.1.0}"
OUTPUT_DIR="${2:-$REPO_ROOT/packaging/dist}"
IDENTIFIER="${TOWEL_MACOS_PKG_IDENTIFIER:-io.jodaro.towel}"
APP_SIGN_IDENTITY="${MACOS_APP_SIGNING_IDENTITY:-}"
PKG_SIGN_IDENTITY="${MACOS_INSTALLER_SIGNING_IDENTITY:-}"

if ! command -v pkgbuild >/dev/null 2>&1; then
  echo "pkgbuild is required and only available on macOS."
  exit 1
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/towel-pkg.XXXXXX")"
PAYLOAD_DIR="$TMP_DIR/payload"
APP_DIR="$PAYLOAD_DIR/Towel.app"
TEMPLATE_DIR="$SCRIPT_DIR/template/Towel.app"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

mkdir -p "$PAYLOAD_DIR"
cp -R "$TEMPLATE_DIR" "$APP_DIR"
cp "$REPO_ROOT/install_mac.sh" "$APP_DIR/Contents/Resources/install_mac.sh"
cp "$REPO_ROOT/install.yml" "$APP_DIR/Contents/Resources/install.yml"

/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $VERSION" "$APP_DIR/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $VERSION" "$APP_DIR/Contents/Info.plist"

chmod 755 "$APP_DIR/Contents/MacOS/Towel"
chmod 755 "$APP_DIR/Contents/Resources/start_towel.command"
chmod 755 "$APP_DIR/Contents/Resources/stop_towel.command"
chmod 755 "$APP_DIR/Contents/Resources/install_mac.sh"

if [[ -n "$APP_SIGN_IDENTITY" ]]; then
  codesign --force --timestamp --sign "$APP_SIGN_IDENTITY" "$APP_DIR"
fi

mkdir -p "$OUTPUT_DIR"

PKGBUILD_ARGS=(
  --root "$PAYLOAD_DIR"
  --install-location /Applications
  --identifier "$IDENTIFIER"
  --version "$VERSION"
  --scripts "$SCRIPT_DIR/scripts"
)

if [[ -n "$PKG_SIGN_IDENTITY" ]]; then
  PKGBUILD_ARGS+=(--sign "$PKG_SIGN_IDENTITY")
fi

pkgbuild "${PKGBUILD_ARGS[@]}" "$OUTPUT_DIR/towel-macos-$VERSION.pkg"
echo "Created $OUTPUT_DIR/towel-macos-$VERSION.pkg"
