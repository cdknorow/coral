#!/bin/bash
# Build coral-go Windows portable ZIP (cross-compiled from Linux/macOS)
#
# Usage: ./installers/build-windows.sh [version]
#   Set CORAL_TIER=dev|beta to select build tier. Default: prod (license required).
# Output: installers/dist/coral-windows-amd64.zip

set -euo pipefail

VERSION="${1:-dev}"
CORAL_TIER="${CORAL_TIER:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$PROJECT_DIR/coral-go"
DIST_DIR="$SCRIPT_DIR/dist"
BUILD_DIR="$DIST_DIR/coral-windows"

echo "==> Building coral-go for Windows (amd64) v${VERSION}"

# Build tags — select tier via CORAL_TIER env var
BUILD_TAGS=""
if [ "$CORAL_TIER" = "dev" ]; then
    BUILD_TAGS="-tags dev"
    echo "==> Tier: dev (EULA skipped, license skipped)"
elif [ "$CORAL_TIER" = "dropboxers" ]; then
    BUILD_TAGS="-tags dropboxers"
    echo "==> Tier: dropboxers (license skipped, 3 teams / 12 agents)"
elif [ "$CORAL_TIER" = "beta" ]; then
    BUILD_TAGS="-tags beta"
    echo "==> Tier: beta (license skipped, demo limits enforced)"
else
    echo "==> Tier: prod (license required)"
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

cd "$GO_DIR"

echo "==> Compiling coral.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral.exe" ./cmd/coral/

echo "==> Compiling launch-coral.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/launch-coral.exe" ./cmd/launch-coral/

echo "==> Compiling coral-board.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-board.exe" ./cmd/coral-board/

echo "==> Compiling coral-agent.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-agent.exe" ./cmd/coral-agent/

for hook in coral-hook-agentic-state coral-hook-task-sync coral-hook-message-check; do
    echo "==> Compiling $hook.exe"
    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/$hook.exe" "./cmd/$hook/"
done

echo "==> Creating ZIP"
cd "$DIST_DIR"
rm -f "coral-windows-amd64-${VERSION}.zip"
zip -j "coral-windows-amd64-${VERSION}.zip" coral-windows/*.exe

echo "==> Cleaning up build dir"
rm -rf "$BUILD_DIR"

echo ""
echo "Done! Installer at: $DIST_DIR/coral-windows-amd64-${VERSION}.zip"
echo ""
echo "To test on Windows:"
echo "  1. Unzip to a folder (e.g. C:\\coral\\)"
echo "  2. Run: coral.exe --backend pty --host 127.0.0.1 --port 8420"
echo "  3. Open browser to http://127.0.0.1:8420"
