#!/usr/bin/env bash
# tools/devops/cask.sh VER: one "cask: Coral VER" commit on main (#4605). The DMG's sha256 comes from the release's
# SHA256SUMS.txt and is compared with the Homebrew tap's (the release workflow updates the tap). The commit is made in a
# throwaway worktree on origin/main and pushed; the shared checkout's Casks/coral.rb is synced only if nobody edited it.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
V=${1:?usage: cask.sh VER}
cd "$MAIN_TREE"; git fetch -q origin
BASE=$(git rev-parse origin/main); [ "$(git rev-parse main)" = "$BASE" ] || die "local main differs from origin/main"
SHA=$(gh release download "v$V" -p SHA256SUMS.txt -O - | awk -v f="Coral.v$V.dmg" '$2==f{print $1}')
[ ${#SHA} = 64 ] || die "no DMG sha256 in v$V's SHA256SUMS.txt"
TAP=$(gh api repos/cdknorow/homebrew-coral/contents/Casks/coral.rb -q .content | base64 -d | awk -F'"' '/^  sha256 /{print $2}')
[ "$TAP" = "$SHA" ] && echo "tap matches $SHA" || echo "NOTE: the tap's sha256 $TAP differs from the release's $SHA"
W=$(mktemp -d /tmp/cask.XXXX)/wt
git worktree add -q --detach "$W" "$BASE"
trap 'git -C "$MAIN_TREE" worktree remove --force "$W" 2>/dev/null || true' EXIT
cd "$W"
echo "cask was $(awk -F'"' '/^  version /{print $2}' Casks/coral.rb)"
[ "$(grep -cE '^  version "[0-9.]+"$' Casks/coral.rb)" = 1 ] && [ "$(grep -cE '^  sha256 "[0-9a-f]{64}"$' Casks/coral.rb)" = 1 ] || die "version or sha256 line not found"
sed -i.bak -e "s/^  version \"[0-9.]*\"$/  version \"$V\"/" -e "s/^  sha256 \"[0-9a-f]\{64\}\"$/  sha256 \"$SHA\"/" Casks/coral.rb && rm -f Casks/coral.rb.bak
[ "$(git diff --name-only)" = Casks/coral.rb ] || die "unexpected change"
git add Casks/coral.rb
git "${IDENT[@]}" commit -q -m "cask: Coral $V

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
C=$(git rev-parse HEAD); cd "$MAIN_TREE"
have=$(shasum -a 256 < Casks/coral.rb | cut -d' ' -f1); b=$(git show "$BASE:Casks/coral.rb" | shasum -a 256 | cut -d' ' -f1)
[ "$have" = "$b" ] || die "the shared tree's Casks/coral.rb was edited; stop"
git push -q origin "$C:refs/heads/main"
git update-ref refs/heads/main "$C" "$BASE"
git show "$C:Casks/coral.rb" > Casks/coral.rb; git reset -q -- Casks/coral.rb
echo "main is now $(git rev-parse --short HEAD): cask: Coral $V"
