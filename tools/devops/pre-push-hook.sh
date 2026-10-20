#!/usr/bin/env bash
# The git pre-push hook for every checkout and worktree of this repository (#5444): a push that adds a built program or a file over
# 5 MB is refused before anything leaves the machine (tools/devops/check_binaries.py). Installed by tools/devops/install-pre-push-hook.sh
# into the shared hooks folder, which all worktrees use. It reads "<local ref> <local sha> <remote ref> <remote sha>" lines on stdin.
# It fails closed: with no installed checker or no python3 the push is refused and the message says how to fix it. git push --no-verify skips it,
# as for any hook: that is a decision for the person pushing.
zero=0000000000000000000000000000000000000000
# The checker is the copy the installer put beside this hook (coral-5444/), not a file of whatever branch happens to be checked out: a worktree on an old
# branch, or one that has just emptied its folder, must not turn the check off. If it is not there the push is refused, not let through (#5444 review);
# git push --no-verify is the one deliberate way round.
chk="$(cd "$(dirname "$0")" && pwd)/coral-5444/check_binaries.py"
[ -f "$chk" ] && command -v python3 >/dev/null 2>&1 || { echo "pre-push: the binary check is not installed here (run tools/devops/install-pre-push-hook.sh) or python3 is missing: push refused; git push --no-verify skips it" >&2; exit 1; }
while read -r lref lsha rref rsha; do
  [ "$lsha" = "$zero" ] && continue                      # a deletion pushes no commits
  if [ "$rsha" != "$zero" ] && git cat-file -e "$rsha^{commit}" 2>/dev/null; then
    python3 -I "$chk" "$rsha" "$lsha" >/dev/null || { python3 -I "$chk" "$rsha" "$lsha" >&2; echo "pre-push: $lref refused (built program or file over 5 MB); fix the commit, do not push it" >&2; exit 1; }
  else
    # a new branch, or one whose remote commit is not here: every commit that no remote-tracking ref holds is checked (also a branch with no common
    # history at all, which has no merge base to compare with)
    git rev-list "$lsha" --not --remotes | python3 -I "$chk" --commits >/dev/null || { git rev-list "$lsha" --not --remotes | python3 -I "$chk" --commits >&2; echo "pre-push: $lref refused (built program or file over 5 MB); fix the commit, do not push it" >&2; exit 1; }
  fi
done
exit 0
