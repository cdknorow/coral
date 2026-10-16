#!/usr/bin/env bash
# Regenerate the Windows icon resources (rsrc_windows_amd64.syso) linked into the user-facing
# exes, from assets/icons/coral.ico (see make_windows_icon.py). The GOOS/GOARCH suffix keeps
# the .syso out of every other platform's build. Commit the results.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GOBIN="$(mktemp -d)"
trap 'rm -rf "$GOBIN"' EXIT
GOBIN="$GOBIN" go install github.com/tc-hib/go-winres@v0.3.3
for cmd in coral launch-coral coral-tray coral-app; do
  (cd "$ROOT/coral-go/cmd/$cmd" && "$GOBIN/go-winres" simply --arch amd64 --icon "$ROOT/assets/icons/coral.ico" --out rsrc)
done
