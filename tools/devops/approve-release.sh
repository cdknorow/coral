#!/usr/bin/env bash
# tools/devops/approve-release.sh check|flip|revoke TAG: the three steps of .github/workflows/approve-release.yml (release channels, #5551/#5559).
#
#   check TAG   read only: the tag is a plain vX.Y.Z, its release is published, a pre-release, immutable and not revoked, its tag commit is
#               on main, and it is NEWER than the current latest (approval only moves forward: a late approval of an older tag would move
#               the stable download links and Homebrew backwards).
#   flip TAG    make it the Approved release: prerelease off, marked latest, and a display-only "Approved: <UTC RFC3339>" line appended to
#               the release body (the client reads the LAST body line, so nothing follows the time: the actor is in the run's summary;
#               every decision is made from the prerelease flag and releases/latest, never from that line).
#   revoke TAG  the only way down, and only for the release that is latest: prerelease on, the highest earlier approved release marked latest again (GitHub does not move the mark by
#               itself), and a "Revoked: <UTC> by <actor>" line in the body, which makes check refuse that tag for ever.
#
# TAG is data from a workflow input: it is matched here before any use and never goes through a shell as text. Needs gh, jq.
# Env: GH_TOKEN, GITHUB_REPOSITORY (default: the current release home), GITHUB_ACTOR (the person who dispatched the run).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$here/../release-homes.sh"
REPO="${GITHUB_REPOSITORY:-$(release_home_current)}"
ACTOR="${GITHUB_ACTOR:-unknown}"
die() { echo "approve-release: $*" >&2; exit 1; }

valid_tag() { [[ "${1:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
# semver_gt A B: success when version A is greater than version B (both plain vX.Y.Z)
semver_gt() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "${1#v}" "${2#v}" | sort -V | tail -1)" = "${1#v}" ]; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
body_append() {  # body_append TAG LINE: add one line to the release body (an immutable release keeps its body editable)
  local f; f="$(mktemp)"
  { gh release view "$1" --repo "$REPO" --json body -q .body; printf '\n%s\n' "$2"; } > "$f"
  gh release edit "$1" --repo "$REPO" --notes-file "$f" >/dev/null || { rm -f "$f"; return 1; }
  rm -f "$f"
}

cmd="${1:-}"; TAG="${2:-}"
valid_tag "$TAG" || die "'$TAG' is not a plain vX.Y.Z tag (suffix tags can never be approved)"

# The read-only checks, run by `check` (the verify job) and again by `flip` (the job that holds contents: write): the release can change between two jobs.
do_check() {
    rel="$(gh api "repos/$REPO/releases/tags/$TAG")" || die "no release for $TAG"
    [ "$(jq -r .draft <<<"$rel")" = false ] || die "$TAG is still a draft"
    [ "$(jq -r .prerelease <<<"$rel")" = true ] || die "$TAG is not a pre-release (already approved?)"
    [ "$(jq -r .immutable <<<"$rel")" = true ] || die "$TAG is not an immutable release"
    body="$(jq -r .body <<<"$rel")"   # not a pipe into grep -q: an early exit would fail jq under pipefail and hide the match
    if grep -q '^Revoked: ' <<<"$body"; then die "$TAG was revoked and cannot be approved"; fi
    sha="$(gh api "repos/$REPO/git/ref/tags/$TAG" --jq .object.sha)" || die "no tag $TAG"
    st="$(gh api "repos/$REPO/compare/main...$sha" --jq .status)" || die "cannot compare $TAG with main"
    case "$st" in behind|identical) ;; *) die "the commit of $TAG is not on main (compare says $st)" ;; esac
    cur="$(gh api "repos/$REPO/releases/latest" --jq .tag_name)" || die "cannot read the current latest"
    valid_tag "$cur" || die "the current latest '$cur' is not a plain tag"
    semver_gt "$TAG" "$cur" || die "$TAG is not newer than the current latest $cur (approval only moves forward)"
    echo "ok: $TAG is a published, immutable pre-release on main, newer than $cur"
}

case "$cmd" in
  check) do_check ;;
  flip)
    do_check >/dev/null
    gh release edit "$TAG" --repo "$REPO" --prerelease=false --latest >/dev/null
    body_append "$TAG" "Approved: $(now)" || echo "warning: the display-only Approved line could not be written" >&2
    echo "approved: $TAG is now the latest release"
    ;;
  revoke)
    rel="$(gh api "repos/$REPO/releases/tags/$TAG")" || die "no release for $TAG"
    [ "$(jq -r .prerelease <<<"$rel")" = false ] || die "$TAG is not approved"
    # Only the release that IS latest can be revoked: revoking an older approved release would re-mark a still older one as latest and move the stable
    # links, Homebrew and every client BACKWARDS past a newer approved release.
    cur="$(gh api "repos/$REPO/releases/latest" --jq .tag_name)" || die "cannot read the current latest"
    [ "$cur" = "$TAG" ] || die "$TAG is not the latest release ($cur is): only the latest release can be revoked"
    prev="$(gh api "repos/$REPO/releases?per_page=30" --jq '.[]|select(.draft==false and .prerelease==false)|.tag_name' | while read -r t; do
      if valid_tag "$t" && [ "$t" != "$TAG" ] && semver_gt "$TAG" "$t"; then echo "$t"; fi; done | sed 's/^v//' | sort -V | tail -1)"
    [ -n "$prev" ] || die "no earlier approved release to fall back to"
    gh release edit "$TAG" --repo "$REPO" --prerelease --latest=false >/dev/null
    gh release edit "v$prev" --repo "$REPO" --latest >/dev/null
    body_append "$TAG" "Revoked: $(now) by $ACTOR" || echo "warning: the Revoked line could not be written" >&2
    echo "revoked: $TAG; v$prev is the latest release again"
    ;;
  *) die "usage: approve-release.sh check|flip|revoke TAG" ;;
esac
