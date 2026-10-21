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
    echo "==> Tier: dev (EULA skipped)"
elif [ "$CORAL_TIER" = "dropboxers" ]; then
    BUILD_TAGS="-tags dropboxers"
    echo "==> Tier: dropboxers (EULA skipped)"
elif [ "$CORAL_TIER" = "beta" ]; then
    BUILD_TAGS="-tags beta"
    echo "==> Tier: beta (EULA required, same as prod)"
else
    echo "==> Tier: prod (EULA required)"
fi

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# The exes' version information (the name the taskbar and Task Manager show): this version and the
# tier's product name, written into the committed resource files for the build and put back after.
if [ "$VERSION" != "dev" ]; then
    SYSO_BACKUP="$(mktemp -d)"
    for cmd in coral launch-coral coral-tray coral-app; do
        cp "$GO_DIR/cmd/$cmd/rsrc_windows_amd64.syso" "$SYSO_BACKUP/$cmd.syso"
    done
    restore_syso() {
        for cmd in coral launch-coral coral-tray coral-app; do
            cp "$SYSO_BACKUP/$cmd.syso" "$GO_DIR/cmd/$cmd/rsrc_windows_amd64.syso"
        done
        rm -rf "$SYSO_BACKUP"
    }
    trap restore_syso EXIT
    PRODUCT="Coral"
    [ "$CORAL_TIER" = "dropboxers" ] && PRODUCT="Coral For Dropbox"
    "$PROJECT_DIR/tools/gen_windows_resources.sh" "$VERSION" "$PRODUCT"
fi

cd "$GO_DIR"

echo "==> Compiling coral.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral.exe" ./cmd/coral/

echo "==> Compiling launch-coral.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/launch-coral.exe" ./cmd/launch-coral/

echo "==> Compiling coral-board.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-board.exe" ./cmd/coral-board/

echo "==> Compiling coral-agent.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-agent.exe" ./cmd/coral-agent/

for hook in coral-hook-agentic-state coral-hook-task-sync coral-hook-message-check coral-hook-session-start; do
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
