#!/usr/bin/env bash
# tools/devops/promote-copy-check.sh [REF]: the pinned copy of the promote workflow (refs/heads/promote) and the one on main must be byte for byte the same whenever the
# pinned one is changed (#6274: two copies drift). Run it from a checkout of main before and after a change to refs/heads/promote; REF is the other side (default origin/promote).
# Exits 0 when the two files are identical (or when REF has no copy yet), 1 when they differ.
set -euo pipefail
ref="${1:-origin/promote}"
f=".github/workflows/promote.yml"
cd "$(git rev-parse --show-toplevel)"
if ! git cat-file -e "$ref:$f" 2>/dev/null; then echo "promote-copy-check: $ref has no $f yet: nothing to compare"; exit 0; fi
if git show "$ref:$f" | cmp -s - "$f"; then echo "promote-copy-check: $ref and the working copy of $f are identical"; exit 0; fi
echo "promote-copy-check: $ref:$f differs from $f; make them identical before changing the pinned copy" >&2
exit 1
