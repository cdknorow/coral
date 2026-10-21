#!/usr/bin/env bash
# tools/devops/after-release.sh VER [--cask]: after the tag vVER is pushed (#4997): wait for its release run, then run
# finish-release.sh (release body, runtime check, verify-release), then, with --cask, cask.sh. One command from "tag pushed" to
# "verified" (and the cask commit), without the idle hand-offs between the three steps.
#
# Stops at the first failure: a failed release run, a failed verify-release, a cask.sh refusal. Without --cask it stops after the
# verification, because the cask commit moves main: re-read the board before passing --cask. Each step's exit status is read on its
# own (never through a pipe). Run it from the main checkout or any worktree of it, with the pinned cosign on PATH (tools/devops/install-cosign.sh): it refuses to
# start without it, since verify-release would only SKIP the bundles (#5112).
set -o pipefail
V=${1:?usage: after-release.sh VER [--cask]}; CASK=${2:-}
case "$V" in *[!0-9A-Za-z.-]*) echo "VER may hold only letters, digits, . and -" >&2; exit 2 ;; esac
case "$CASK" in ""|--cask) ;; *) echo "usage: after-release.sh VER [--cask]" >&2; exit 2 ;; esac
R=$(git rev-parse --show-toplevel) || exit 1; cd "$R" || exit 1
. "$R/tools/devops/lib.sh"
require_cosign   # before the wait: a release the bundles of which cannot be checked is not verified (#5112)
git fetch -q origin --tags
git rev-parse -q --verify "refs/tags/v$V" >/dev/null || die "no tag v$V (push it first)"
RUN=""
for _ in 1 2 3 4 5 6; do
  RUN=$(gh run list --workflow release.yml --branch "v$V" --limit 1 --json databaseId -q '.[0].databaseId') || die "gh run list failed"
  [ -n "$RUN" ] && break
  sleep 10
done
[ -n "$RUN" ] || die "no release.yml run for v$V after a minute"
echo "release run $RUN: waiting"
gh run watch "$RUN" --exit-status >/dev/null 2>&1; rc=$?
[ "$rc" = 0 ] || die "the release run $RUN did not succeed (rc=$rc): $(gh run view "$RUN" --json conclusion -q .conclusion)"
echo "release run $RUN: success"
LOG=$(mktemp /tmp/after-release.XXXXXX)
bash tools/devops/finish-release.sh "$V" > "$LOG" 2>&1; rc=$?
tail -12 "$LOG"
[ "$rc" = 0 ] || { echo "finish-release failed (rc=$rc), full log: $LOG" >&2; exit 1; }
rm -f -- "$LOG"
echo "v$V is verified"
if [ "$CASK" = --cask ]; then
  bash tools/devops/cask.sh "$V" || die "cask.sh failed"
else
  echo "cask not made: it follows the APPROVAL (the Approve release workflow moves the tap); after the operator approved v$V, re-read the board, then: tools/devops/cask.sh $V"
fi
