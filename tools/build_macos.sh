#!/usr/bin/env bash
# Compatibility entry point for the universal macOS app builder.
# Both native tmux payload slices are required; installers/build-macos.sh
# validates them before any Go build or output cleanup.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
exec "$root/installers/build-macos.sh" "$@"
