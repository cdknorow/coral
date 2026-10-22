#!/bin/bash
# Build coral-go Linux portable tarball
#
# Usage: ./installers/build-linux.sh [version] [amd64|arm64]
#   Set CORAL_TIER=dev|beta to select build tier. Default: prod.
#   The architecture defaults to amd64; arm64 cross-compiles (CGO is off).
# Output: installers/dist/coral-linux-<arch>-<version>.tar.gz

set -euo pipefail

VERSION="${1:-dev}"
ARCH="${2:-amd64}"
case "$ARCH" in amd64|arm64) ;; *) echo "architecture must be amd64 or arm64" >&2; exit 2 ;; esac
CORAL_TIER="${CORAL_TIER:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GO_DIR="$PROJECT_DIR/coral-go"
DIST_DIR="$SCRIPT_DIR/dist"
BUILD_DIR="$DIST_DIR/coral-linux"

echo "==> Building coral-go for Linux (${ARCH}) v${VERSION}"

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

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

cd "$GO_DIR"

for cmd in coral launch-coral coral-board coral-agent coral-hook-agentic-state coral-hook-message-check coral-hook-session-start coral-hook-task-sync coral-update; do
    echo "==> Compiling $cmd"
    GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -trimpath -tags "$BUILD_TAGS" \
        -ldflags="-s -w -X github.com/subgentic/coral/internal/config.Version=$VERSION" \
        -o "$BUILD_DIR/$cmd" "./cmd/$cmd/"
done

"$PROJECT_DIR/tests/release/verify_standard_bundle.sh" "$BUILD_DIR" "$ARCH"

echo "==> Creating tarball"
cd "$DIST_DIR"
TARBALL="coral-linux-${ARCH}-${VERSION}.tar.gz"
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
