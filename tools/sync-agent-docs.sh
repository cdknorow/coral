#!/bin/bash
# Sync agent_docs/ to the embedded static directory.
# Run this before `go build` or `go run` during development.
# Production builds run it too: each build job in .github/workflows/release.yml, before go build.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC="$SCRIPT_DIR/../coral-go/agent_docs"
DST="$SCRIPT_DIR/../coral-go/pkg/frontend/static/agent_docs"

if [ ! -d "$SRC" ]; then
    echo "Error: agent_docs not found at $SRC"
    exit 1
fi

rm -rf "$DST"
mkdir -p "$DST"
# The docs are embedded gzipped (#6814): about 700 KB of Markdown ship as about 260 KB, and the server unzips them for a client that does not accept gzip. -n: no name or time in the
# stream, so the same text always makes the same bytes. Anything that is not a .md file is copied as it is.
count=0
for f in "$SRC"/*; do
    [ -f "$f" ] || continue
    case "$f" in
        *.md) gzip -9 -n -c "$f" > "$DST/$(basename "$f").gz"; count=$((count + 1)) ;;
        *) cp "$f" "$DST/" ;;
    esac
done
echo "Synced agent_docs → static/agent_docs ($count files, gzipped)"
