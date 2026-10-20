#!/usr/bin/env bash
# The git pre-push hook for every checkout and worktree of this repository (#5444): a push that adds a built program or a file over
# 5 MB is refused before anything leaves the machine (tools/devops/check_binaries.py). Installed by tools/devops/install-pre-push-hook.sh
# into the shared hooks folder, which all worktrees use. It reads "<local ref> <local sha> <remote ref> <remote sha>" lines on stdin.
# It never blocks for its own failure to run (no script, no python3): it says so and lets the push go, the checks in the landing
# scripts remain. git push --no-verify skips it, as for any hook: that is a decision for the person pushing.
zero=0000000000000000000000000000000000000000
common=$(git rev-parse --git-common-dir) || exit 0
chk="$(cd "$common/.." && pwd)/tools/devops/check_binaries.py"
[ -f "$chk" ] && command -v python3 >/dev/null 2>&1 || { echo "pre-push: check_binaries.py not found, not checked" >&2; exit 0; }
while read -r lref lsha rref rsha; do
  [ "$lsha" = "$zero" ] && continue                      # a deletion pushes no commits
  if [ "$rsha" != "$zero" ] && git cat-file -e "$rsha^{commit}" 2>/dev/null; then base=$rsha
  else base=$(git merge-base "$lsha" refs/remotes/origin/main 2>/dev/null) || base=""
  fi
  [ -n "$base" ] || { echo "pre-push: no base for $lref, not checked" >&2; continue; }
  python3 -I "$chk" "$base" "$lsha" >/dev/null || { python3 -I "$chk" "$base" "$lsha" >&2; echo "pre-push: $lref refused (built program or file over 5 MB); fix the commit, do not push it" >&2; exit 1; }
done
exit 0
