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

echo "==> Building coral-go for macOS (universal) v${VERSION}"

# Build ldflags — always strip symbols; inject PostHog key and version for all builds
LDFLAGS="-s -w"
POSTHOG_KEY="${CORAL_POSTHOG_KEY:-phc_qXGp75qwDNcETkBDDsptPuP8qAV4nNQPDmTdAC8K9h2}"
LDFLAGS="$LDFLAGS -X github.com/cdknorow/coral/internal/config.PostHogKey=$POSTHOG_KEY -X github.com/cdknorow/coral/internal/config.Version=$VERSION"

# Build tags — select tier via CORAL_TIER env var
BUILD_TAGS=""
if [ "$CORAL_TIER" = "dev" ]; then
    BUILD_TAGS="-tags dev"
    echo "==> Tier: dev (EULA skipped, license skipped, no demo limits)"
elif [ "$CORAL_TIER" = "dropboxers" ]; then
    BUILD_TAGS="-tags dropboxers"
    echo "==> Tier: dropboxers (EULA required, license skipped, 3 teams / 12 agents)"
elif [ "$CORAL_TIER" = "beta" ]; then
    BUILD_TAGS="-tags beta"
    echo "==> Tier: beta (EULA required, license skipped, demo limits enforced)"
else
    echo "==> Tier: prod (EULA required, license required)"
fi

rm -rf "$APP_DIR" "$DIST_DIR/coral-arm64" "$DIST_DIR/coral-amd64"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources" "$DIST_DIR"

# Bundle frontend JS/CSS (minify + combine) before Go embed picks them up
if [ -f "$PROJECT_DIR/scripts/bundle-frontend.sh" ]; then
    bash "$PROJECT_DIR/scripts/bundle-frontend.sh"
fi

cd "$GO_DIR"

# Build arm64
echo "==> Compiling coral (arm64)"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/coral-arm64" ./cmd/coral/

# Build amd64
echo "==> Compiling coral (amd64)"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/coral-amd64" ./cmd/coral/

# Create universal binary
echo "==> Creating universal binary with lipo"
if command -v lipo &>/dev/null; then
    lipo -create -output "$APP_DIR/Contents/MacOS/coral" "$DIST_DIR/coral-arm64" "$DIST_DIR/coral-amd64"
else
    echo "WARNING: lipo not available (not on macOS). Using arm64 binary only."
    cp "$DIST_DIR/coral-arm64" "$APP_DIR/Contents/MacOS/coral"
fi
chmod +x "$APP_DIR/Contents/MacOS/coral"

# Build all pure-Go companion binaries as universal
echo "==> Compiling companion CLI binaries (universal)"
for cmd in launch-coral coral-board coral-agent coral-hook-agentic-state coral-hook-message-check coral-hook-task-sync; do
    if [ -d "./cmd/$cmd" ]; then
        echo "    Building $cmd..."
        GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-arm64" ./cmd/$cmd/
        GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-amd64" ./cmd/$cmd/
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
WEBVIEW_TAGS="-tags webview"
if [ -n "$BUILD_TAGS" ]; then
    TIER_TAG="${BUILD_TAGS#-tags }"
    WEBVIEW_TAGS="-tags webview,${TIER_TAG}"
fi
echo "==> Compiling coral-tray + coral-app (CGO required)"
if [[ "$(uname -s)" == "Darwin" ]]; then
    for cmd in coral-tray coral-app; do
        echo "    Building $cmd..."
        GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build $WEBVIEW_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-arm64" ./cmd/$cmd/
        GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build $WEBVIEW_TAGS -ldflags="$LDFLAGS" -o "$DIST_DIR/${cmd}-amd64" ./cmd/$cmd/
        lipo -create -output "$APP_DIR/Contents/MacOS/$cmd" "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        rm -f "$DIST_DIR/${cmd}-arm64" "$DIST_DIR/${cmd}-amd64"
        chmod +x "$APP_DIR/Contents/MacOS/$cmd"
    done
else
    echo "WARNING: coral-tray and coral-app require macOS (CGO + Cocoa/WebKit). Skipping on $(uname -s)."
fi

# Info.plist
echo "==> Creating Info.plist"
if [ -f "$PROJECT_DIR/scripts/Info.plist" ]; then
    sed "s/VERSION_PLACEHOLDER/${VERSION}/g" "$PROJECT_DIR/scripts/Info.plist" > "$APP_DIR/Contents/Info.plist"
else
    cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>Coral</string>
    <key>CFBundleDisplayName</key><string>Coral</string>
    <key>CFBundleIdentifier</key><string>com.coral.dashboard</string>
    <key>CFBundleVersion</key><string>${VERSION}</string>
    <key>CFBundleShortVersionString</key><string>${VERSION}</string>
    <key>CFBundleExecutable</key><string>coral-tray</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>LSMinimumSystemVersion</key><string>11.0</string>
    <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
fi

# App icon
if [ -f "$PROJECT_DIR/Coral.icns" ]; then
    cp "$PROJECT_DIR/Coral.icns" "$APP_DIR/Contents/Resources/AppIcon.icns"
elif [ -f "$PROJECT_DIR/icons/coral.icns" ]; then
    cp "$PROJECT_DIR/icons/coral.icns" "$APP_DIR/Contents/Resources/AppIcon.icns"
fi

# CLI install script (creates symlinks in /usr/local/bin)
cp "$SCRIPT_DIR/install-cli.sh" "$APP_DIR/Contents/MacOS/install-cli.sh"
chmod +x "$APP_DIR/Contents/MacOS/install-cli.sh"

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
    SCRIPT_DIR_TEST="$(cd "$(dirname "$0")/.." && pwd)/scripts"
    if [ -f "$SCRIPT_DIR_TEST/test-macos-app.sh" ]; then
        bash "$SCRIPT_DIR_TEST/test-macos-app.sh" "$APP_DIR"
    fi
fi

# Restore original JS sources (so working tree stays clean for development)
if [ -f "$PROJECT_DIR/scripts/bundle-frontend.sh" ]; then
    bash "$PROJECT_DIR/scripts/bundle-frontend.sh" --restore
fi

echo ""
echo "Done! Installer at: $DIST_DIR/$DMG_NAME"
echo "App bundle at: $APP_DIR"
