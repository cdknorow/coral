#!/bin/bash
# Build coral-go Linux portable tarball
#
# Usage: ./installers/build-linux.sh [version]
#   Set CORAL_TIER=dev|beta to select build tier. Default: prod (license required).
# Output: installers/dist/coral-linux-amd64-<version>.tar.gz

set -euo pipefail

VERSION="${1:-dev}"
CORAL_TIER="${CORAL_TIER:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$PROJECT_DIR/coral-go"
DIST_DIR="$SCRIPT_DIR/dist"
BUILD_DIR="$DIST_DIR/coral-linux"

echo "==> Building coral-go for Linux (amd64) v${VERSION}"

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

echo "==> Compiling coral"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral" ./cmd/coral/

echo "==> Compiling launch-coral"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/launch-coral" ./cmd/launch-coral/

echo "==> Compiling coral-board"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-board" ./cmd/coral-board/

echo "==> Compiling coral-agent"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $BUILD_TAGS -ldflags="-s -w" -o "$BUILD_DIR/coral-agent" ./cmd/coral-agent/

echo "==> Creating tarball"
cd "$DIST_DIR"
TARBALL="coral-linux-amd64-${VERSION}.tar.gz"
rm -f "$TARBALL"
tar czf "$TARBALL" -C coral-linux .

echo "==> Cleaning up build dir"
rm -rf "$BUILD_DIR"

echo ""
echo "Done! Installer at: $DIST_DIR/$TARBALL"
echo ""
echo "To install:"
echo "  tar xzf $TARBALL -C /usr/local/bin/"
echo "  coral --host 127.0.0.1 --port 8420"
