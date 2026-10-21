#!/usr/bin/env bash
# tools/devops/release-cadence.sh [COUNT]: the release channels' cadence table (#5551, #5559): the latest COUNT (default 12) plain-tag releases,
# newest first, with their state and age.
#
#   approved    published, not a pre-release (the Approved channel); "Approved:" is read from the display-only body line when there is one
#   WAITING     a pre-release NEWER than the newest approved release: published, not yet approved; RED once it has waited 7 days
#   superseded  a pre-release OLDER than the newest approved release: it will never be approved (approval only moves forward)
#   revoked     the body carries a "Revoked:" line
#
# Read only (gh api, anonymous-safe fields). Env: GITHUB_REPOSITORY (default: the current release home), NOW (a fixed UTC time, for the tests).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/../release-homes.sh"
REPO="${GITHUB_REPOSITORY:-$(release_home_current)}"
COUNT="${1:-12}"; [[ "$COUNT" =~ ^[0-9]+$ ]] || { echo "usage: release-cadence.sh [COUNT]" >&2; exit 2; }
now_s() { if [ -n "${NOW:-}" ]; then date -u -j -f %Y-%m-%dT%H:%M:%SZ "$NOW" +%s 2>/dev/null || date -u -d "$NOW" +%s; else date -u +%s; fi; }
to_s() { date -u -j -f %Y-%m-%dT%H:%M:%SZ "$1" +%s 2>/dev/null || date -u -d "$1" +%s; }
NOWS="$(now_s)"
json="$(gh api "repos/$REPO/releases?per_page=100")"
# newest approved version among plain tags (by version, not by date)
newest="$(jq -r '.[]|select(.draft==false and .prerelease==false)|.tag_name' <<<"$json" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -V | tail -1 || true)"
printf '%-10s %-11s %-21s %-21s %s\n' TAG STATE PUBLISHED APPROVED WAITING
jq -r '.[]|select(.draft==false)|select(.tag_name|test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))|[.tag_name,(.prerelease|tostring),.published_at,((.body // "")|gsub("\n";"\u001f"))]|@tsv' <<<"$json" \
  | sort -t$'\t' -k1,1 -V -r | head -n "$COUNT" | while IFS=$'\t' read -r tag pre pub body; do
  body="${body//$'\x1f'/$'\n'}"
  appr="-"; wait="-"; state=approved
  if grep -q '^Revoked: ' <<<"$body"; then state=revoked
  elif [ "$pre" = true ]; then
    if [ -n "$newest" ] && [ "$(printf '%s\n%s\n' "${tag#v}" "$newest" | sort -V | tail -1)" = "$newest" ]; then state=superseded
    else
      state=WAITING; d=$(( (NOWS - $(to_s "$pub")) / 86400 )); wait="${d} days"; [ "$d" -ge 7 ] && wait="$wait (RED: over a week)"
    fi
  else
    appr="$(grep -m1 '^Approved: ' <<<"$body" | sed -E 's/^Approved: ([0-9TZ:-]+).*/\1/' || true)"; [ -n "$appr" ] || appr="(no marker)"
  fi
  printf '%-10s %-11s %-21s %-21s %s\n' "$tag" "$state" "$pub" "$appr" "$wait"
done
