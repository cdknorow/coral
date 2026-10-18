# shellcheck shell=bash disable=SC2034  # sourced by the other scripts, which use MAIN_TREE and IDENT
# tools/devops/lib.sh: shared helpers for the landing and release scripts (sourced, not run).
# MAIN_TREE is the main checkout (the shared tree), found from any worktree; IDENT is the landing identity.
set -o pipefail
MAIN_TREE=$(cd "$(git rev-parse --git-common-dir)/.." && pwd)
IDENT=(-c user.name="${CORAL_LAND_NAME:-Coral DevOps stack}" -c user.email="${CORAL_LAND_EMAIL:-devops-stack@example.test}")
die() { echo "$*" >&2; exit 1; }
# scan_patch FILE: refuse a patch that carries personal data or binaries (placeholder paths like /Users/someone are fine).
scan_patch() {
  local hits
  hits=$(grep -nE "${CORAL_SCAN_PATTERN:-/Users/[^/ ]+/|^Binary files .* differ$|^GIT binary patch}" "$1" | grep -vE '/Users/(someone|alice|bob|me|user)/' || true)
  [ -z "$hits" ] || { echo "$hits" | head -5; return 1; }
}
