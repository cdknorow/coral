#!/usr/bin/env bash
# tools/devops/promote-copy-check.sh: identical copies pass, a differing copy fails, a ref with no copy yet passes (#6274).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d /tmp/pcc.XXXXXX)"
trap 'rm -rf "$T"' EXIT
cd "$T"
git init -q . && git config user.email t@example.invalid && git config user.name t
mkdir -p .github/workflows tools/devops
cp "$here/../../tools/devops/promote-copy-check.sh" tools/devops/
echo "name: x" > .github/workflows/promote.yml
git add -A && git commit -qm one
git branch promote
bash tools/devops/promote-copy-check.sh promote >/dev/null || { echo "FAIL: identical copies were refused" >&2; exit 1; }
echo "name: y" > .github/workflows/promote.yml
if bash tools/devops/promote-copy-check.sh promote >/dev/null 2>&1; then echo "FAIL: a differing copy was accepted" >&2; exit 1; fi
bash tools/devops/promote-copy-check.sh nobranch >/dev/null || { echo "FAIL: a ref with no copy yet was refused" >&2; exit 1; }
echo "PASS promote_copy_check"
