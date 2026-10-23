#!/bin/bash
# Build coral-go macOS universal .app bundle + DMG
#
# Usage: ./installers/build-macos.sh [version]
#   Set CORAL_TIER=dev|beta to select build tier. Default: prod (license required).
# Output: installers/dist/Coral-<version>.dmg

set -euo pipefail

VERSION="${1:-dev}"
CORAL_TIER="${CORAL_TIER:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$PROJECT_DIR/coral-go"
DIST_DIR="$SCRIPT_DIR/dist"
APP_DIR="$DIST_DIR/Coral.app"

# This local universal build cannot compile the non-native tmux C slice. Feed
# it the verified output directories from native arm64 and Intel build jobs.
: "${CORAL_TMUX_ARM64_PAYLOAD:?Set CORAL_TMUX_ARM64_PAYLOAD to a native arm64 tmux payload directory (from tools/build-bundled-tmux.sh or the release CI artifact)}"
: "${CORAL_TMUX_X86_64_PAYLOAD:?Set CORAL_TMUX_X86_64_PAYLOAD to a native x86_64 tmux payload directory (from tools/build-bundled-tmux.sh or the release CI artifact)}"
CORAL_TMUX_ARM64_PAYLOAD="$(cd "$CORAL_TMUX_ARM64_PAYLOAD" && pwd -P)"
CORAL_TMUX_X86_64_PAYLOAD="$(cd "$CORAL_TMUX_X86_64_PAYLOAD" && pwd -P)"
for spec in "arm64:$CORAL_TMUX_ARM64_PAYLOAD" "x86_64:$CORAL_TMUX_X86_64_PAYLOAD"; do
    arch="${spec%%:*}"
    payload="${spec#*:}"
    test -x "$payload/bin/tmux" || { echo "Missing $arch tmux at $payload/bin/tmux" >&2; exit 1; }
    "$PROJECT_DIR/tests/release/verify_bundled_tmux_slice.sh" "$payload" "$arch"
done
diff -qr "$CORAL_TMUX_ARM64_PAYLOAD/terminfo" "$CORAL_TMUX_X86_64_PAYLOAD/terminfo"
diff -qr "$CORAL_TMUX_ARM64_PAYLOAD/licenses" "$CORAL_TMUX_X86_64_PAYLOAD/licenses"

echo "==> Building coral-go for macOS (universal) v${VERSION}"

# Build ldflags — always strip symbols and inject the version. A local build has NO analytics key unless CORAL_POSTHOG_KEY is set (#5184: the script used to
# embed the project's key for every tier, dev builds too, so a build made here sent usage events); the release workflow injects its own key from a secret.
LDFLAGS="-s -w -X github.com/subgentic/coral/internal/config.Version=$VERSION"
if [ -n "${CORAL_POSTHOG_KEY:-}" ]; then
    LDFLAGS="$LDFLAGS -X github.com/subgentic/coral/internal/config.PostHogKey=$CORAL_POSTHOG_KEY"
else
    echo "==> No CORAL_POSTHOG_KEY: this build sends no usage analytics"
fi

# Build tags — select tier via CORAL_TIER env var
BUILD_TAGS="fts5"
if [ "$CORAL_TIER" = "dev" ]; then
    BUILD_TAGS+=",dev"
    echo "==> Tier: dev (EULA skipped)"
elif [ "$CORAL_TIER" = "dropboxers" ]; then
    BUILD_TAGS+=",dropboxers"
    echo "==> Tier: dropboxers (EULA skipped)"
elif [ "$CORAL_TIER" = "beta" ]; then
    BUILD_TAGS+=",beta"
    echo "==> Tier: beta (EULA required, same as prod)"
else
    echo "==> Tier: prod (EULA required)"
fi

rm -rf "$APP_DIR" "$DIST_DIR/coral-arm64" "$DIST_DIR/coral-amd64"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources" "$DIST_DIR"

cd "$GO_DIR"

OVERLAY="$(go run ./tools/strip-static)" # the dashboard ships minified (#6208): a fresh owner-only folder; only the go build commands below read it
[ -n "$OVERLAY" ] || { echo "strip-static printed no overlay path" >&2; exit 1; }
trap 'go run ./tools/strip-static -clean "$OVERLAY"' EXIT

# Build arm64
echo "==> Compiling coral (arm64)"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -overlay "$OVERLAY" -tags "$BUILD_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/coral-arm64" ./cmd/coral/

# Build amd64
echo "==> Compiling coral (amd64)"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -overlay "$OVERLAY" -tags "$BUILD_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/coral-amd64" ./cmd/coral/

# Create universal binary
echo "==> Creating universal binary with lipo"
if command -v lipo &>/dev/null; then
    lipo -create -output "$APP_DIR/Contents/MacOS/coral" "$DIST_DIR/coral-arm64" "$DIST_DIR/coral-amd64"
else
    echo "WARNING: lipo not available (not on macOS). Using arm64 binary only."
    cp "$DIST_DIR/coral-arm64" "$APP_DIR/Contents/MacOS/coral"
fi
chmod +x "$APP_DIR/Contents/MacOS/coral"

lipo -create -output "$APP_DIR/Contents/MacOS/tmux" "$CORAL_TMUX_ARM64_PAYLOAD/bin/tmux" "$CORAL_TMUX_X86_64_PAYLOAD/bin/tmux"
chmod +x "$APP_DIR/Contents/MacOS/tmux"
cp -R "$CORAL_TMUX_ARM64_PAYLOAD/terminfo" "$APP_DIR/Contents/Resources/terminfo"
cp -R "$CORAL_TMUX_ARM64_PAYLOAD/licenses" "$APP_DIR/Contents/Resources/tmux-licenses"

# Build all pure-Go companion binaries as universal
echo "==> Compiling companion CLI binaries (universal)"
for cmd in launch-coral coral-board coral-agent coral-hook-agentic-state coral-hook-message-check coral-hook-session-start coral-hook-task-sync coral-update; do
    if [ -d "./cmd/$cmd" ]; then
        echo "    Building $cmd..."
        GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -overlay "$OVERLAY" -tags "$BUILD_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-arm64" ./cmd/$cmd/
        GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -overlay "$OVERLAY" -tags "$BUILD_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-amd64" ./cmd/$cmd/
        if command -v lipo &>/dev/null; then
            lipo -create -output "$APP_DIR/Contents/MacOS/$cmd" "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
            rm -f "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        else
            cp "$DIST_DIR/${cmd}-arm64" "$APP_DIR/Contents/MacOS/$cmd"
            rm -f "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        fi
        chmod +x "$APP_DIR/Contents/MacOS/$cmd"
    fi
done

# Build CGO binaries (tray + webview app — require native platform APIs)
# Combine webview tag with tier tag if present
WEBVIEW_TAGS="webview,${BUILD_TAGS}"
echo "==> Compiling coral-tray + coral-app (CGO required)"
if [[ "$(uname -s)" == "Darwin" ]]; then
    for cmd in coral-tray coral-app; do
        echo "    Building $cmd..."
        MACOSX_DEPLOYMENT_TARGET=13.0 CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -trimpath -overlay "$OVERLAY" -tags "$WEBVIEW_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-arm64" ./cmd/$cmd/
        MACOSX_DEPLOYMENT_TARGET=13.0 CGO_CFLAGS="-mmacosx-version-min=13.0" CGO_LDFLAGS="-mmacosx-version-min=13.0" GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -trimpath -overlay "$OVERLAY" -tags "$WEBVIEW_TAGS" -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-amd64" ./cmd/$cmd/
        lipo -create -output "$APP_DIR/Contents/MacOS/$cmd" "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        rm -f "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        chmod +x "$APP_DIR/Contents/MacOS/$cmd"
    done
else
    echo "WARNING: coral-tray and coral-app require macOS (CGO + Cocoa/WebKit). Skipping on $(uname -s)."
fi

# Info.plist
echo "==> Creating Info.plist"
sed "s/VERSION_PLACEHOLDER/${VERSION}/g" "$PROJECT_DIR/tools/Info.plist" > "$APP_DIR/Contents/Info.plist"

# App icon
if [ -f "$PROJECT_DIR/Coral.icns" ]; then
    cp "$PROJECT_DIR/Coral.icns" "$APP_DIR/Contents/Resources/AppIcon.icns"
elif [ -f "$PROJECT_DIR/icons/coral.icns" ]; then
    cp "$PROJECT_DIR/icons/coral.icns" "$APP_DIR/Contents/Resources/AppIcon.icns"
fi

# CLI install script (creates symlinks in /usr/local/bin)
cp "$SCRIPT_DIR/install-cli.sh" "$APP_DIR/Contents/MacOS/install-cli.sh"
chmod +x "$APP_DIR/Contents/MacOS/install-cli.sh"

"$PROJECT_DIR/tests/release/verify_standard_bundle.sh" "$APP_DIR/Contents/MacOS"

# Cleanup temp binaries
rm -f "$DIST_DIR/coral-arm64" "$DIST_DIR/coral-amd64"

# Create DMG
echo "==> Creating DMG"
DMG_NAME="Coral-${VERSION}.dmg"
rm -f "$DIST_DIR/$DMG_NAME"
if command -v hdiutil &>/dev/null; then
    hdiutil create -volname "Coral" -srcfolder "$APP_DIR" -ov -format UDZO "$DIST_DIR/$DMG_NAME"
else
    echo "WARNING: hdiutil not available (not on macOS). Creating tar.gz instead."
    cd "$DIST_DIR" && tar czf "Coral-${VERSION}.tar.gz" Coral.app
    DMG_NAME="Coral-${VERSION}.tar.gz"
fi

# Optional smoke test
if [ "${SMOKE_TEST:-}" = "1" ]; then
    echo "==> Running smoke test..."
    SCRIPT_DIR_TEST="$(cd "$(dirname "$0")/.." && pwd)/tools"
    if [ -f "$SCRIPT_DIR_TEST/test-macos-app.sh" ]; then
        bash "$SCRIPT_DIR_TEST/test-macos-app.sh" "$APP_DIR"
    fi
fi

echo ""
echo "Done! Installer at: $DIST_DIR/$DMG_NAME"
echo "App bundle at: $APP_DIR"
