#!/usr/bin/env bash
# verify_no_fake_identity.sh passes a package of clean programs, fails one holding the rehearsal's fake identity (in any program), and refuses an empty package (#6290).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d /tmp/nofake.XXXXXX)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/ok" "$T/bad" "$T/none"
first="$(grep -vE '^[[:space:]]*(#|$)' "$here/commands.txt" | head -1)"
second="$(grep -vE '^[[:space:]]*(#|$)' "$here/commands.txt" | sed -n 2p)"
[ -n "$first" ] && [ -n "$second" ] || { echo "FAIL: commands.txt needs two programs for this test" >&2; exit 1; }
for d in ok bad; do printf 'ELF....clean program....' > "$T/$d/$first"; printf 'ELF....another clean program....' > "$T/$d/$second"; done
"$here/verify_no_fake_identity.sh" "$T/ok" >/dev/null || { echo "FAIL: a clean package was refused" >&2; exit 1; }
printf 'ELF....https://oidc.rehearsal.invalid....' > "$T/bad/$second"
set +e; out="$("$here/verify_no_fake_identity.sh" "$T/bad" 2>&1)"; rc=$?; set -e
[ "$rc" = 1 ] || { echo "FAIL: a program holding the fake identity gave exit $rc, want 1" >&2; exit 1; }
case "$out" in *"$second"*"fake identity"*) ;; *) echo "FAIL: it did not name the program: $out" >&2; exit 1 ;; esac
set +e; "$here/verify_no_fake_identity.sh" "$T/none" >/dev/null 2>&1; rc=$?; set -e
[ "$rc" = 2 ] || { echo "FAIL: an empty package gave exit $rc, want 2" >&2; exit 1; }
echo "PASS verify_no_fake_identity: a clean package passes, a fake identity in any program fails, an empty package is refused"
