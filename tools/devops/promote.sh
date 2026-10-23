#!/usr/bin/env bash
# tools/devops/promote.sh inputs|release|sums|previous|check|publish: the steps of .github/workflows/promote.yml (#326).
#
# A promotion is a release of its own in this repository, because the product releases are immutable and can take no new file:
#   promote-<sequence>  promote.json and its Sigstore bundle; the sequence is the highest published one plus one, so the updater finds the
#                       newest by probing the names (coral-go/internal/updater/stage/promotion.go)
#   promote-v<version>  the first promotion of that version, for the Windows rollback lookup
# Both are pre-releases that are never marked latest: Latest stays the product release people download, and GitHub never falls back to a
# pre-release as Latest. The sums and the packages stay in the product release v<version>; the manifest only names it.
#
#   inputs    MODE, TAG and REVOKE are well formed
#   release   TAG is an approved product release (published, immutable, not a pre-release, never revoked; the latest, except for backfill);
#             for revoke, each version revoked carries approve-release's "Revoked:" line
#   sums      SHA256SUMS.txt of TAG, verified against release.yml's identity for exactly that tag and the tag's commit (an annotated tag peeled)
#   previous  the highest promote-<n>, its signature verified and n equal to its sequence (none on the first promotion)
#   check     the built promote.json is for TAG and takes the next sequence (in the job that signs, before it signs)
#   publish   promote-<sequence> (and promote-v<version> the first time), then Latest read back unchanged and the manifest read back
# TAG and REVOKE are data from workflow inputs: they are matched here before any use and never go through a shell as text.
# Needs gh, jq and cosign. Env: GH_TOKEN, GITHUB_REPOSITORY, MODE, TAG, REVOKE.
set -euo pipefail
REPO="${GITHUB_REPOSITORY:-subgentic/coral}"
OIDC_ISSUER="https://token.actions.githubusercontent.com"
PROMOTE_ID="https://github.com/$REPO/.github/workflows/promote.yml@refs/heads/main"
die() { echo "::error::promote: $*" >&2; exit 1; }
valid_tag() { [[ "${1:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
valid_mode() { [[ "${1:-}" =~ ^(promote|refresh|revoke|backfill)$ ]]; }
mkdir -p work

# verify_promote FILE BUNDLE: signed by promote.yml on main, dispatched by hand (the identity the updater pins)
verify_promote() {
  cosign verify-blob "$1" --bundle "$2" --certificate-identity "$PROMOTE_ID" --certificate-oidc-issuer "$OIDC_ISSUER" \
    --certificate-github-workflow-repository "$REPO" --certificate-github-workflow-ref refs/heads/main \
    --certificate-github-workflow-trigger workflow_dispatch >/dev/null
}

# tag_commit TAG: the commit a tag points at, an annotated tag peeled
tag_commit() {
  local ref type sha
  ref="$(gh api "repos/$REPO/git/ref/tags/$1")" || return 1
  type="$(jq -r .object.type <<<"$ref")"; sha="$(jq -r .object.sha <<<"$ref")"
  if [ "$type" = tag ]; then sha="$(gh api "repos/$REPO/git/tags/$sha" --jq .object.sha)" || return 1; fi
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] && echo "$sha"
}

# highest_promotion: the highest n of a promote-<n> tag (0 when there is none)
highest_promotion() {
  gh api "repos/$REPO/git/matching-refs/tags/promote-" --paginate --jq '.[].ref' |
    sed -n 's#^refs/tags/promote-\([1-9][0-9]*\)$#\1#p' | sort -n | tail -1 | grep . || echo 0
}

cmd="${1:-}"
case "$cmd" in
  inputs)
    valid_mode "${MODE:-}" || die "mode is not promote, refresh, revoke or backfill"
    valid_tag "${TAG:-}" || die "the tag is not a plain vX.Y.Z"
    for r in ${REVOKE:-}; do [[ "$r" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "revoke: '$r' is not a plain X.Y.Z"; done
    if [ "$MODE" = revoke ] && [ -z "${REVOKE:-}" ]; then die "mode revoke needs the versions to revoke"; fi
    if [ "$MODE" != revoke ] && [ -n "${REVOKE:-}" ]; then die "revoke is only for mode revoke"; fi
    echo "ok: $MODE $TAG ${REVOKE:-}"
    ;;

  release)
    valid_mode "${MODE:-}" && valid_tag "${TAG:-}" || die "bad inputs"
    rel="$(gh api "repos/$REPO/releases/tags/$TAG")" || die "no release for $TAG"
    [ "$(jq -r .draft <<<"$rel")" = false ] || die "$TAG is a draft"
    [ "$(jq -r .prerelease <<<"$rel")" = false ] || die "$TAG is a pre-release: run Approve release first"
    [ "$(jq -r .immutable <<<"$rel")" = true ] || die "$TAG is not an immutable release"
    body="$(jq -r .body <<<"$rel")"   # not a pipe into grep -q: an early exit would fail jq under pipefail
    if grep -q '^Revoked: ' <<<"$body"; then die "$TAG was revoked"; fi
    if [ "$MODE" != backfill ]; then
      latest="$(gh api "repos/$REPO/releases/latest" --jq .tag_name)"
      [ "$latest" = "$TAG" ] || die "$TAG is not the latest release ($latest is)"
    fi
    for r in ${REVOKE:-}; do   # a revoke follows approve-release's revoke of that release
      rb="$(gh api "repos/$REPO/releases/tags/v$r" --jq .body)" || die "no release for v$r"
      grep -q '^Revoked: ' <<<"$rb" || die "v$r carries no 'Revoked:' line: run Approve release (mode revoke) for it first"
    done
    echo "ok: $TAG is an approved release"
    ;;

  sums)
    valid_tag "${TAG:-}" || die "bad tag"
    commit="$(tag_commit "$TAG")" || die "no commit for $TAG"
    gh release download "$TAG" --repo "$REPO" --pattern SHA256SUMS.txt --pattern SHA256SUMS.txt.sigstore.json --dir work --clobber
    cosign verify-blob work/SHA256SUMS.txt --bundle work/SHA256SUMS.txt.sigstore.json \
      --certificate-identity "https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$TAG" --certificate-oidc-issuer "$OIDC_ISSUER" \
      --certificate-github-workflow-repository "$REPO" --certificate-github-workflow-ref "refs/tags/$TAG" \
      --certificate-github-workflow-trigger push --certificate-github-workflow-sha "$commit" >/dev/null
    if grep -Evq '^[0-9a-f]{64}  \*?[^/\\]+$' work/SHA256SUMS.txt; then die "SHA256SUMS.txt has a line that is not '<sha256>  <name>'"; fi
    v="${TAG#v}"
    for a in "Coral-$v-x64.msi" "Coral.v$v.dmg" "coral-linux-amd64-$v.tar.gz" "coral-linux-arm64-$v.tar.gz"; do
      n="$(awk -v f="$a" '$2==f || $2=="*"f {c++} END {print c+0}' work/SHA256SUMS.txt)"
      [ "$n" = 1 ] || die "$a is listed $n times in SHA256SUMS.txt"
    done
    echo "$commit" > work/source_sha
    echo "ok: the sums file of $TAG is release.yml's, from $commit"
    ;;

  previous)
    mkdir -p work/previous
    n="$(highest_promotion)"
    echo "$n" > work/previous/sequence
    if [ "$n" = 0 ]; then echo "no promotion yet"; exit 0; fi
    gh release download "promote-$n" --repo "$REPO" --pattern promote.json --pattern promote.json.sigstore.json --dir work/previous --clobber
    verify_promote work/previous/promote.json work/previous/promote.json.sigstore.json || die "promote-$n's signature does not verify"
    [ "$(jq -r .sequence work/previous/promote.json)" = "$n" ] || die "promote-$n carries another sequence"
    rm work/previous/promote.json.sigstore.json
    echo "previous: promote-$n $(jq -c '{tag, expires_at, revoked}' work/previous/promote.json)"
    ;;

  check)
    valid_mode "${MODE:-}" && valid_tag "${TAG:-}" || die "bad inputs"
    [ "$(jq -r .tag promote.json)" = "$TAG" ] || die "the manifest is not for $TAG"
    [ "$(jq -r .source_sha promote.json)" = "$(cat work/source_sha)" ] || die "the manifest's commit is not the verified one"
    prev="$(cat work/previous/sequence)"; seq="$(jq -r .sequence promote.json)"
    if [ "$MODE" = backfill ]; then want=$(( prev > 0 ? prev : 1 )); else want=$(( prev + 1 )); fi
    [ "$seq" = "$want" ] || die "the manifest's sequence $seq is not $want"
    [ "$(highest_promotion)" = "$prev" ] || die "a promotion was published since the build: run it again"
    echo "ok: promote.json is for $TAG, sequence $seq"
    ;;

  publish)
    valid_mode "${MODE:-}" && valid_tag "${TAG:-}" || die "bad inputs"
    verify_promote promote.json promote.json.sigstore.json || die "the signed manifest does not verify"
    seq="$(jq -r .sequence promote.json)"; [[ "$seq" =~ ^[1-9][0-9]*$ ]] || die "bad sequence"
    version="${TAG#v}"
    before="$(gh api "repos/$REPO/releases/latest" --jq .tag_name)"
    # publish_release TAGNAME: a draft with both files, then published (an immutable release takes no file once published), a pre-release
    # never marked latest. A draft left by a failed run under the same name goes first.
    publish_release() {
      gh api "repos/$REPO/releases" --paginate --jq ".[] | select(.draft and .tag_name == \"$1\") | .id" | while read -r id; do
        [[ "$id" =~ ^[0-9]+$ ]] && gh api -X DELETE "repos/$REPO/releases/$id" >/dev/null
      done
      gh release create "$1" --repo "$REPO" --target "$(cat work/source_sha)" --draft --prerelease --latest=false \
        --title "$1" --notes "Promotion of $TAG (sequence $seq), signed by promote.yml. Not a product release: the updater reads it." \
        promote.json promote.json.sigstore.json >/dev/null
      gh release edit "$1" --repo "$REPO" --draft=false --prerelease --latest=false >/dev/null
    }
    if [ "$MODE" != backfill ]; then publish_release "promote-$seq"; fi
    if ! gh release view "promote-v$version" --repo "$REPO" >/dev/null 2>&1; then publish_release "promote-v$version"; fi
    after="$(gh api "repos/$REPO/releases/latest" --jq .tag_name)"
    [ "$after" = "$before" ] || die "Latest moved from $before to $after: put it back (gh release edit $before --latest) and report it"
    if [ "$MODE" != backfill ]; then
      curl -fsSL "https://github.com/$REPO/releases/download/promote-$seq/promote.json" -o work/readback.json
      cmp -s work/readback.json promote.json || die "promote-$seq's promote.json is not the manifest just signed"
    fi
    {
      echo "### Promotion"
      echo "- mode: $MODE"
      echo "- tag: $TAG"
      echo "- sequence: $seq"
      echo "- latest (unchanged): $after"
      echo "- run by: ${GITHUB_ACTOR:-unknown}"
    } >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
    echo "published: $TAG as promote-$seq"
    ;;

  *) die "usage: promote.sh inputs|release|sums|previous|check|publish" ;;
esac
