#!/usr/bin/env bash
# tools/devops/land-main.sh BASE TARGET GATEDTREE: fast-forward main from BASE to TARGET and push it (#4605).
# TARGET must be a notes-only commit whose parent's tree is GATEDTREE (the tree the gate tested), and origin/main and the
# local main must both be BASE. The shared checkout's files that TARGET changes are synced only when they still match BASE
# (anyone else's edit stops the landing), each with the mode the tree gives it; a file TARGET deletes is removed by path.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
cd "$MAIN_TREE"; git fetch -q origin
base=$(git rev-parse "$1"); C=$(git rev-parse "$2"); gated=$3
[ "$(git rev-parse origin/main)" = "$base" ] || die "origin/main moved"
[ "$(git rev-parse main)" = "$base" ] || die "local main moved"
git merge-base --is-ancestor "$base" "$C" || die "not a fast-forward"
[ "$(git rev-parse "$C^^{tree}")" = "$gated" ] || die "the parent's tree is not the gated tree"
[ "$(git diff --name-only "$C^" "$C")" = RELEASE_NOTES.md ] || die "the top commit is not notes-only"
FILES=(); while IFS= read -r f; do FILES+=("$f"); done < <(git diff --no-renames --name-only "$base" "$C")
SYNC=()
for f in "${FILES[@]}"; do
  if git cat-file -e "$C:$f" 2>/dev/null; then want=$(git show "$C:$f" | shasum -a 256 | cut -d' ' -f1); else want=missing; fi
  if [ -f "$f" ]; then have=$(shasum -a 256 < "$f" | cut -d' ' -f1); else have=missing; fi
  if git cat-file -e "$base:$f" 2>/dev/null; then b=$(git show "$base:$f" | shasum -a 256 | cut -d' ' -f1); else b=missing; fi
  if [ "$have" = "$want" ]; then :; elif [ "$have" = "$b" ]; then SYNC+=("$f"); else die "someone else's edit in the shared tree: $f"; fi
done
echo "files: ${#FILES[@]}, to sync: ${#SYNC[@]}"
check_binaries "$base" "$C"   # the landing refuses a program or a 5 MB file (#5444)
git push -q origin "$C:refs/heads/main"
git update-ref refs/heads/main "$C" "$base"
for f in "${SYNC[@]}"; do
  if git cat-file -e "$C:$f" 2>/dev/null; then
    mkdir -p "$(dirname "$f")"; git show "$C:$f" > "$f"
    if [ "$(git ls-tree "$C" -- "$f" | cut -c1-6)" = 100755 ]; then chmod +x "$f"; else chmod -x "$f"; fi
  else rm -f -- "$f"; fi
done
[ ${#SYNC[@]} -gt 0 ] && git reset -q -- "${SYNC[@]}"
[ -z "$(git diff --summary | grep 'mode change' || true)" ] || die "mode drift after sync"
echo "main is now $(git rev-parse --short HEAD)"
