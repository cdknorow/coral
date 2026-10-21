#!/usr/bin/env bash
# tools/devops/update-tap.sh VERSION SHA256 [--revoke]: one new commit on the Homebrew tap's main that sets the cask to VERSION (release channels, #5559).
# The cask is an APPROVED-channel surface: this runs from approve-release.yml, never at publication.
#
#   VERSION  plain X.Y.Z; SHA256 the DMG's line of the Sigstore-verified SHA256SUMS.txt (64 hex digits)
#   without --revoke: never a downgrade (a cask already at VERSION or newer is left alone, exit 0)
#   with --revoke:    VERSION must be OLDER than the cask's: the cask goes back by a new commit (never a force-push)
#
# Also rewrites the cask's download URL and homepage to GITHUB_REPOSITORY (the tap stayed at its owner when the repository moved).
# Env: TAP_URL (default git@github.com:cdknorow/homebrew-coral.git), GITHUB_REPOSITORY, TAP_HEADER (an http extraheader for a token),
# GIT_SSH_COMMAND (set by the caller for a deploy key). The identity of the commit is the one fixed below.
set -euo pipefail
die() { echo "update-tap: $*" >&2; exit 1; }
VERSION="${1:-}"; SHA="${2:-}"; MODE="${3:-}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION '$VERSION' is not a plain X.Y.Z"
[[ "$SHA" =~ ^[0-9a-f]{64}$ ]] || die "SHA256 is not 64 lower-case hex digits"
case "$MODE" in ""|--revoke) ;; *) die "usage: update-tap.sh VERSION SHA256 [--revoke]" ;; esac
REPO="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
TAP_URL="${TAP_URL:-git@github.com:cdknorow/homebrew-coral.git}"
export GIT_AUTHOR_NAME=coral-release GIT_AUTHOR_EMAIL=coral-release@users.noreply.github.com
export GIT_COMMITTER_NAME=coral-release GIT_COMMITTER_EMAIL=coral-release@users.noreply.github.com
g() { if [ -n "${TAP_HEADER:-}" ]; then git -c http.extraheader="$TAP_HEADER" "$@"; else git "$@"; fi; }
W="$(mktemp -d)"; trap 'rm -rf "$W"' EXIT
g clone -q --depth 1 "$TAP_URL" "$W/tap"
cd "$W/tap"
CUR="$(sed -nE 's/^  version "([^"]*)"$/\1/p' Casks/coral.rb)"
[[ "$CUR" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the cask has no plain version line"
if [ "$CUR" = "$VERSION" ]; then echo "cask already at $VERSION"; exit 0; fi
newest="$(printf '%s\n%s\n' "$CUR" "$VERSION" | sort -V | tail -1)"
if [ "$MODE" = --revoke ]; then
  [ "$newest" = "$CUR" ] || die "revoke: $VERSION is not older than the cask's $CUR"
else
  if [ "$newest" != "$VERSION" ]; then echo "cask is at $CUR, newer than $VERSION; not downgrading"; exit 0; fi
fi
sed -i.bak -E "s/^  version \"[^\"]*\"$/  version \"${VERSION}\"/; s/^  sha256 \"[^\"]*\"$/  sha256 \"${SHA}\"/" Casks/coral.rb
# the download URL and the homepage follow the repository that built the release
sed -i.bak -E "s#^  url \"https://github.com/[^/\"]+/[^/\"]+/releases/download/#  url \"https://github.com/${REPO}/releases/download/#; s#^  homepage \"https://github.com/[^\"]*\"#  homepage \"https://github.com/${REPO}\"#" Casks/coral.rb
rm -f Casks/coral.rb.bak
if ! { grep -qx "  version \"${VERSION}\"" Casks/coral.rb && grep -qx "  sha256 \"${SHA}\"" Casks/coral.rb \
       && grep -qx "  url \"https://github.com/${REPO}/releases/download/v#{version}/Coral.v#{version}.dmg\"" Casks/coral.rb; }; then
  die "the cask was not updated as expected"
fi
if [ "$MODE" = --revoke ]; then msg="coral ${VERSION} (revoke: back from ${CUR})"; else msg="coral ${VERSION}"; fi
git commit -qam "$msg"
if ! g push -q origin HEAD:main; then
  g pull -q --rebase origin main
  g push -q origin HEAD:main
fi
echo "tap: the cask is at $VERSION (was $CUR)"
