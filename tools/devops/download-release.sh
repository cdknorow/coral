#!/usr/bin/env bash
# dl_release.sh TAG DIR: download every asset of TAG into DIR, retrying each until its sha256 equals GitHub's digest
set -o pipefail
TAG=$1; DIR=$2; mkdir -p "$DIR"; cd "$(git rev-parse --show-toplevel)" || exit 1
gh api repos/cdknorow/coral/releases/tags/$TAG -q '.assets[]|"\(.name) \(.digest)"' > "$DIR/.digests" || exit 1
while read -r name digest; do
  want=${digest#sha256:}; ok=0
  for try in 1 2 3 4 5 6; do
    rm -f "$DIR/$name"
    gh release download "$TAG" -R cdknorow/coral -p "$name" -D "$DIR" >/dev/null 2>&1
    got=$(shasum -a 256 "$DIR/$name" 2>/dev/null | cut -d' ' -f1)
    if [ "$got" = "$want" ]; then ok=1; echo "ok   $name (try $try)"; break; fi
  done
  [ $ok = 1 ] || { echo "FAIL $name after 6 tries"; exit 1; }
done < "$DIR/.digests"
rm -f "$DIR/.digests"
