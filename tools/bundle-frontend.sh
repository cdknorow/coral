#!/bin/bash
# Bundle and minify frontend JS/CSS for production builds.
# Combines all ES module JS into a single minified file.
#
# Usage: scripts/bundle-frontend.sh [--restore]
# Requires: esbuild (brew install esbuild)
#
# The script:
# 1. Copies original .js sources to _src/ (backup)
# 2. Bundles app.js + all imports into a single minified app.js
# 3. Removes individual module .js files (bundled into app.js)
# 4. Minifies CSS
#
# Use --restore to undo bundling and restore original sources.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
STATIC_DIR="$SCRIPT_DIR/../coral-go/pkg/frontend/static"
SRC_DIR="$STATIC_DIR/_src"

# ── Restore mode ─────────────────────────────────────────────
if [ "${1:-}" = "--restore" ]; then
    if [ ! -d "$SRC_DIR" ]; then
        echo "Nothing to restore (_src/ not found)"
        exit 0
    fi
    echo "==> Restoring original sources"
    cp "$SRC_DIR"/*.js "$STATIC_DIR/"
    # Restore platform/ module directory
    if [ -d "$SRC_DIR/platform" ]; then
        rm -rf "$STATIC_DIR/platform"
        cp -r "$SRC_DIR/platform" "$STATIC_DIR/platform"
    fi
    rm -rf "$SRC_DIR"
    rm -f "$STATIC_DIR/app.js.map"
    echo "    Done"
    exit 0
fi

# ── Bundle mode ──────────────────────────────────────────────
if ! command -v esbuild &>/dev/null; then
    # Try common locations
    for p in /opt/homebrew/bin/esbuild /usr/local/bin/esbuild; do
        if [ -x "$p" ]; then
            alias esbuild="$p"
            ESBUILD="$p"
            break
        fi
    done
    if [ -z "${ESBUILD:-}" ]; then
        echo "WARNING: esbuild not found, skipping JS bundling"
        exit 0
    fi
else
    ESBUILD="esbuild"
fi

# ── Sync agent_docs ─────────────────────────────────────────
bash "$SCRIPT_DIR/sync-agent-docs.sh"

echo "==> Bundling frontend JS/CSS"

# Backup original sources (including platform/ subdirectory)
if [ ! -d "$SRC_DIR" ]; then
    mkdir -p "$SRC_DIR"
    for f in "$STATIC_DIR"/*.js; do
        [ -f "$f" ] && cp "$f" "$SRC_DIR/"
    done
    # Backup platform/ module directory
    if [ -d "$STATIC_DIR/platform" ]; then
        cp -r "$STATIC_DIR/platform" "$SRC_DIR/platform"
    fi
fi

# Bundle app.js with all imports into single minified file
"$ESBUILD" "$SRC_DIR/app.js" \
    --bundle \
    --minify \
    --sourcemap \
    --target=es2020 \
    --format=esm \
    --outfile="$STATIC_DIR/app.js" \
    2>&1 | grep -v "^$"

# Remove individual module .js files (now in the bundle)
for f in "$STATIC_DIR"/*.js; do
    base="$(basename "$f")"
    case "$base" in
        app.js|app.js.map) continue ;;
        *) rm -f "$f" ;;
    esac
done
# Remove platform/ directory (bundled into app.js)
rm -rf "$STATIC_DIR/platform"

# Minify CSS
for cssfile in "$STATIC_DIR"/*.css; do
    [ -f "$cssfile" ] || continue
    "$ESBUILD" "$cssfile" --minify --outfile="$cssfile" --allow-overwrite 2>/dev/null
done

# Report
ORIG_SIZE=$(find "$SRC_DIR" -name '*.js' -exec cat {} + 2>/dev/null | wc -c | tr -d ' ')
BUNDLE_SIZE=$(wc -c < "$STATIC_DIR/app.js" | tr -d ' ')
echo "    JS: ${ORIG_SIZE} → ${BUNDLE_SIZE} bytes ($(( (ORIG_SIZE - BUNDLE_SIZE) * 100 / ORIG_SIZE ))% smaller)"
echo "==> Done"
