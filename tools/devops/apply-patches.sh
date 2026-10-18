#!/usr/bin/env bash
# tools/devops/apply-patches.sh WORKTREE PATCH...: apply mbox patches in order with git am --3way --keep-cr (#4605).
# --keep-cr keeps carriage returns in raw terminal captures (plain git am strips them; v1.4.87 lost six PTY fixtures).
# Each patch is scanned first (personal data, binaries; set CORAL_ALLOW_BINARY=1 for a patch whose binaries are known).
# A conflict on tests/frontend/package.json's test:cloud line is resolved as the union of both sides; any other conflict
# stops the run with the files listed. After each patch: no conflict markers.
set -o pipefail
W=$(cd "${1:?usage: apply-patches.sh WORKTREE PATCH...}" && pwd); shift
cd "$W" && . "$W/tools/devops/lib.sh"
[ "$(git rev-parse --show-toplevel)" = "$W" ] || die "not a worktree root: $W"
[ -z "$(git status --porcelain)" ] || die "worktree not clean"
for f in "$@"; do
  if [ -n "${CORAL_ALLOW_BINARY:-}" ]; then CORAL_SCAN_PATTERN='/Users/[^/ ]+/' scan_patch "$f" || die "scan refused $f"
  else scan_patch "$f" || die "scan refused $f"; fi
  if ! git "${IDENT[@]}" am -q --3way --keep-cr "$f"; then
    U=$(git diff --name-only --diff-filter=U)
    [ "$U" = tests/frontend/package.json ] || die "conflict in $f: $U (resolve by hand, then git am --continue)"
    python3 -I "$W/tools/devops/union-testcloud.py" "$U" && node -e 'JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"))' "$U" || die "union failed"
    git add "$U" && GIT_EDITOR=true git "${IDENT[@]}" am --continue || die "am --continue failed"
  fi
  ! git grep -nE '^(<<<<<<<|>>>>>>>)' -- . ':!*.md' || die "conflict markers after $f"
  echo "applied $(basename "$f") -> $(git rev-parse --short HEAD)"
done
