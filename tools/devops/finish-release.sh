#!/usr/bin/env bash
# tools/devops/finish-release.sh VER: after the release workflow published vVER (#4605): set the release body from the
# tag's RELEASE_NOTES.md section, start the runtime check, download every asset with a per-file retry until it matches
# GitHub's own digest (a dropped connection truncated downloads twice on 2026-10-18), then run verify-release.sh on that
# folder (--local). cosign must be on PATH (verify-release's cosign rows SKIP without it).
set -o pipefail
V=${1:?usage: finish-release.sh VER}; R=$(git rev-parse --show-toplevel); cd "$R" || exit 1
git fetch -q origin --tags
B=$(mktemp /tmp/body.XXXX)
git show "v$V:RELEASE_NOTES.md" | awk -v h="## v$V " 'index($0,h)==1{f=1;next} f&&/^## v[0-9]/{exit} f' | awk 'NF{p=1} p' > "$B"
[ -s "$B" ] || { echo "no notes section for v$V at the tag"; exit 1; }
gh release edit "v$V" --notes-file "$B" >/dev/null || exit 1; echo "body set ($(wc -l < "$B" | tr -d ' ') lines)"; rm -f "$B"
gh workflow run 373415903 -f tag="v$V" && sleep 6 && echo "runtime check: $(gh run list --workflow 373415903 --limit 1 --json databaseId -q '.[0].databaseId')"
D=$(mktemp -d /tmp/rel.XXXX); T=$(mktemp -d /tmp/vr.XXXX); trap 'rm -rf "$D" "$T"' EXIT
bash "$R/tools/devops/download-release.sh" "v$V" "$D" || exit 1
TMPDIR=$T bash tools/verify-release.sh "v$V" --local "$D"
