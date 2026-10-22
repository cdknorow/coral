#!/usr/bin/env bash
# The table of tests/release/signature_required_check.sh (N4): it must pass on this branch's switch, and fail exactly when a required signature meets a build that is not signed.
set -uo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
script="$root/tests/release/signature_required_check.sh"
real="$root/coral-go/internal/updater/apply/authenticode_require.go"
work=$(mktemp -d /tmp/sigreq.XXXX); case "$work" in /tmp/sigreq.????) ;; *) echo "bad temp dir"; exit 1;; esac
trap 'rm -rf "$work"' EXIT
fail=0
mk() { printf 'package apply\n\nconst RequireSignedWindowsPackage = %s\n' "$1" > "$work/$2.go"; }
mk false off; mk true on
check() { # name want-exit file signing tags
  bash "$script" "$3" "$4" "$5" >/dev/null 2>&1; got=$?
  if [ "$got" != "$2" ]; then echo "FAIL: $1: exit $got, wanted $2"; fail=1; else echo "ok: $1"; fi
}
check "this branch's real file passes with signing off" 0 "$real" "" "fts5"
check "switch off, unsigned build" 0 "$work/off.go" "" "fts5"
check "switch off, Dropbox build" 0 "$work/off.go" "false" "fts5,dropboxers"
check "switch on, signed prod build" 0 "$work/on.go" "true" "fts5"
check "switch on, signed beta build" 0 "$work/on.go" "true" "fts5,beta"
check "switch on, signing not switched on" 1 "$work/on.go" "" "fts5"
check "switch on, WINDOWS_SIGNING=false" 1 "$work/on.go" "false" "fts5"
check "switch on, WINDOWS_SIGNING=TRUE (not exactly true)" 1 "$work/on.go" "TRUE" "fts5"
check "switch on, Dropbox build even when signing is on" 1 "$work/on.go" "true" "fts5,dropboxers"
printf 'package apply\n' > "$work/none.go"; check "no switch line is an error, not a pass" 2 "$work/none.go" "true" "fts5"
printf 'const RequireSignedWindowsPackage = true\nconst RequireSignedWindowsPackage = false\n' > "$work/two.go"; check "two switch lines is an error" 2 "$work/two.go" "true" "fts5"
check "no such file is an error" 2 "$work/missing.go" "true" "fts5"
# the real switch is the one the check reads: it is false here
grep -q '^const RequireSignedWindowsPackage = false' "$real" || { echo "FAIL: the real switch is not false"; fail=1; }
[ "$fail" = 0 ] && echo "signature_required_check: all cases pass"
exit "$fail"
