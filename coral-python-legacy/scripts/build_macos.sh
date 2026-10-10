#!/usr/bin/env bash
# Build Coral.app and Coral.dmg for macOS distribution (Go binary).
#
# Prerequisites:
#   - Go 1.23+ installed
#   - brew install create-dmg   (optional, for .dmg creation)
#
# Usage:
#   scripts/build_macos.sh [version]
#
# Examples:
#   scripts/build_macos.sh           # builds with version from git tag or "dev"
#   scripts/build_macos.sh 2.3.1     # builds with explicit version

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$PROJECT_DIR/coral-go"
cd "$PROJECT_DIR"

APP_NAME="Coral"
ICON_PNG="$GO_DIR/pkg/frontend/static/coral.png"
ICON_ICNS="$PROJECT_DIR/Coral.icns"

# Determine version
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    VERSION="$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "dev")"
fi
echo "=== Building ${APP_NAME}.app v${VERSION} ==="

# ── Step 1: Generate .icns from coral.png ──────────────────────────
if [ ! -f "$ICON_ICNS" ]; then
    echo "-> Generating ${ICON_ICNS} from ${ICON_PNG}..."
    if [ -f "$ICON_PNG" ] && command -v sips &>/dev/null && command -v iconutil &>/dev/null; then
        ICONSET_DIR="${APP_NAME}.iconset"
        mkdir -p "$ICONSET_DIR"
        for size in 16 32 64 128 256 512; do
            sips -z $size $size "$ICON_PNG" --out "${ICONSET_DIR}/icon_${size}x${size}.png" &>/dev/null
        done
        for size in 32 64 128 256 512 1024; do
            half=$((size / 2))
            sips -z $size $size "$ICON_PNG" --out "${ICONSET_DIR}/icon_${half}x${half}@2x.png" &>/dev/null
        done
        iconutil -c icns "$ICONSET_DIR" -o "$ICON_ICNS"
        rm -rf "$ICONSET_DIR"
        echo "  OK Created ${ICON_ICNS}"
    else
        echo "  WARN sips/iconutil not available or icon not found -- skipping .icns generation"
        echo "       The app will build without a custom icon."
    fi
fi

# ── Step 2: Clean previous builds ─────────────────────────────────
echo "-> Cleaning previous builds..."
rm -rf dist
mkdir -p dist

# ── Step 3: Cross-compile Go binaries ─────────────────────────────
echo "-> Building Go binary for darwin/arm64..."
cd "$GO_DIR"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$PROJECT_DIR/dist/coral-arm64" \
    ./cmd/coral/

echo "-> Building Go binary for darwin/amd64..."
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o "$PROJECT_DIR/dist/coral-amd64" \
    ./cmd/coral/

cd "$PROJECT_DIR"

# ── Step 4: Create universal binary ───────────────────────────────
echo "-> Creating universal binary with lipo..."
if command -v lipo &>/dev/null; then
    lipo -create -output dist/coral dist/coral-arm64 dist/coral-amd64
    echo "  OK Universal binary created"
else
    echo "  WARN lipo not available -- using arm64 binary only"
    cp dist/coral-arm64 dist/coral
fi

# Clean up arch-specific binaries
rm -f dist/coral-arm64 dist/coral-amd64

# ── Step 5: Construct .app bundle ─────────────────────────────────
echo "-> Constructing ${APP_NAME}.app bundle..."
APP_DIR="dist/${APP_NAME}.app"
CONTENTS="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS/MacOS"
RESOURCES="$CONTENTS/Resources"

mkdir -p "$MACOS_DIR" "$RESOURCES"

# Copy binary
cp dist/coral "$MACOS_DIR/coral"
chmod +x "$MACOS_DIR/coral"

# Generate Info.plist from template
sed "s/VERSION_PLACEHOLDER/${VERSION}/g" "$SCRIPT_DIR/Info.plist" > "$CONTENTS/Info.plist"

# Copy icon
if [ -f "$ICON_ICNS" ]; then
    cp "$ICON_ICNS" "$RESOURCES/AppIcon.icns"
fi

# Remove standalone binary (it's inside the .app now)
rm -f dist/coral

echo "  OK Built dist/${APP_NAME}.app"

# ── Step 6: Create .dmg ───────────────────────────────────────────
if command -v create-dmg &>/dev/null; then
    DMG_NAME="${APP_NAME}.dmg"
    echo "-> Creating ${DMG_NAME}..."
    rm -f "dist/${DMG_NAME}"

    VOLICON_ARG=""
    if [ -f "$ICON_ICNS" ]; then
        VOLICON_ARG="--volicon $ICON_ICNS"
    fi

    create-dmg \
        --volname "$APP_NAME" \
        ${VOLICON_ARG} \
        --window-pos 200 120 \
        --window-size 600 400 \
        --icon-size 100 \
        --icon "${APP_NAME}.app" 175 190 \
        --app-drop-link 425 190 \
        --no-internet-enable \
        "dist/${DMG_NAME}" \
        "$APP_DIR"
    echo "  OK Created dist/${DMG_NAME}"
else
    echo "  WARN create-dmg not found -- skipping .dmg creation"
    echo "       Install with: brew install create-dmg"
fi

echo ""
echo "=== Build complete ==="
echo "  Version: ${VERSION}"
echo "  App:     dist/${APP_NAME}.app"
[ -f "dist/${APP_NAME}.dmg" ] && echo "  DMG:     dist/${APP_NAME}.dmg"
