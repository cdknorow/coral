#!/usr/bin/env bash
# #6290: the updater rehearsal's fake identities (internal/updater/testrig: an issuer and a repository under "rehearsal.invalid", a name that cannot exist) are for tests only. No program of a package may hold one:
# a binary that does has linked the test rig, and its pins could be satisfied by a signer that is not GitHub's. Usage: verify_no_fake_identity.sh PACKAGE_DIR   (every program named in commands.txt and gui_commands.txt that is there is searched)
set -euo pipefail
marker="rehearsal.invalid"
package_dir="$(cd "${1:?usage: verify_no_fake_identity.sh PACKAGE_DIR}" && pwd -P)"
here="$(cd "$(dirname "$0")" && pwd)"
programs=(); while IFS= read -r c; do programs+=("$c"); done < <(cat "$here/commands.txt" "$here/gui_commands.txt" 2>/dev/null | grep -vE '^[[:space:]]*(#|$)' | sort -u)
[ "${#programs[@]}" -gt 0 ] || { echo "verify_no_fake_identity.sh: commands.txt lists no programs" >&2; exit 2; }
found=0
for p in "${programs[@]}"; do
  for f in "$package_dir/$p" "$package_dir/$p.exe"; do
    [ -f "$f" ] || continue
    found=$((found + 1))
    if grep -aFq -- "$marker" "$f"; then
      echo "verify_no_fake_identity.sh: $f holds the rehearsal's fake identity ($marker): it linked internal/updater/testrig" >&2
      exit 1
    fi
  done
done
# a package with none of the programs is not a package that was checked
[ "$found" -gt 0 ] || { echo "verify_no_fake_identity.sh: none of the programs in commands.txt is in $package_dir" >&2; exit 2; }
echo "verify_no_fake_identity: $found programs, none holds a fake identity"
