#!/usr/bin/env bash
# tools/devops/finish-release.sh VER: after the release workflow published vVER (#4605): set the release body from the
# tag's RELEASE_NOTES.md section, start the runtime check, then run verify-release.sh. It takes the files from the
# release run's artifacts and checks each against GitHub's digest of the published asset, so nothing is downloaded from
# the release (#4936; a download there counts as a user download). The pinned cosign must be on PATH (#5112): it refuses to start without it, and
# verify-release runs with --require-cosign, so a missing cosign or a release with no bundles is a FAIL row, never a SKIP.
set -o pipefail
V=${1:?usage: finish-release.sh VER}; R=$(git rev-parse --show-toplevel); cd "$R" || exit 1
. "$R/tools/devops/lib.sh"; require_cosign || exit 1
git fetch -q origin --tags
B=$(mktemp /tmp/body.XXXX)
git show "v$V:RELEASE_NOTES.md" | awk -v h="## v$V " 'index($0,h)==1{f=1;next} f&&/^## v[0-9]/{exit} f' | awk 'NF{p=1} p' > "$B"
[ -s "$B" ] || { echo "no notes section for v$V at the tag"; exit 1; }
# The Download section (#5467) goes above the notes: GitHub hides the disk image behind "Show more assets". Its links are checked with HEAD
# requests (a HEAD is not a download) before the body is set; a release without stable names (before 1.4.130) gets the notes alone.
if release_has_stable_names "$V"; then
  D=$(bash "$R/tools/devops/download-section.sh" "$V") || exit 1
  for u in $(echo "$D" | grep -o 'https://[^)]*'); do
    c=$(curl -sIL -o /dev/null -w '%{http_code}' "$u"); [ "$c" = 200 ] || { echo "download link $u answers $c"; exit 1; }
  done
  echo "download section: $(echo "$D" | grep -o "https://[^)]*" | wc -l | tr -d " ") links answer 200"
  { echo "$D"; cat "$B"; } > "$B.full" && mv "$B.full" "$B"
fi
gh release edit "v$V" --notes-file "$B" >/dev/null || exit 1; echo "body set ($(wc -l < "$B" | tr -d ' ') lines)"; rm -f "$B"
gh workflow run 373415903 -f tag="v$V" && sleep 6 && echo "runtime check: $(gh run list --workflow 373415903 --limit 1 --json databaseId -q '.[0].databaseId')"
T=$(mktemp -d /tmp/vr.XXXX); trap 'rm -rf "$T"' EXIT
TMPDIR=$T bash tools/verify-release.sh "v$V" --require-cosign
