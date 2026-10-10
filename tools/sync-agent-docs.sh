#!/bin/bash
# Sync agent_docs/ to the embedded static directory.
# Run this before `go build` or `go run` during development.
# Production builds run this automatically via bundle-frontend.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC="$SCRIPT_DIR/../coral-go/agent_docs"
DST="$SCRIPT_DIR/../coral-go/pkg/frontend/static/agent_docs"

if [ ! -d "$SRC" ]; then
    echo "Error: agent_docs not found at $SRC"
    exit 1
fi

rm -rf "$DST"
cp -r "$SRC" "$DST"
echo "Synced agent_docs → static/agent_docs ($(find "$DST" -name '*.md' | wc -l | tr -d ' ') files)"
