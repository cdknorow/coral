#!/usr/bin/env bash
# verify-release.sh refuses a coral binary that holds the dashboard's source comments (built without the strip-static overlay, #6208).
# The test runs the script's own helper on a stripped binary and on a mutant that carries the marker, and checks every call site is wired.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
script="$here/../../tools/verify-release.sh"
T="$(mktemp -d /tmp/vrstrip.XXXXXX)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/tools"
{ echo "here=\"$(cd "$(dirname "$script")" && pwd)\""; sed -n '/^STRIP_MARKER=/,/^}/p' "$script"; } > "$T/helper.sh"
[ -s "$T/helper.sh" ] || { echo "FAIL: helper not found in verify-release.sh" >&2; exit 1; }
marker="$(head -n1 "$here/strip_marker.txt")"
printf 'ELF....clean binary....' > "$T/stripped"
printf 'ELF....%s....' "$marker" > "$T/mutant"
SCRIPT="$script"
# shellcheck disable=SC1090
. "$T/helper.sh"
unstripped "$T/stripped" && { echo "FAIL: a stripped binary was refused" >&2; exit 1; }
unstripped "$T/mutant" || { echo "FAIL: a binary holding the source comment was accepted" >&2; exit 1; }
# the marker file is read, and a missing or empty one stops the script (exit 2) instead of passing every binary
[ -n "$STRIP_MARKER" ] || { echo "FAIL: marker not read" >&2; exit 1; }
mkdir -p "$T/fake/tools" "$T/fake/tests/release"
{ echo 'here="$(cd "$(dirname "$0")" && pwd)"'; sed -n '/^STRIP_MARKER=/,/^}/p' "$script"; } > "$T/fake/tools/guard.sh"
for state in missing empty; do
  rm -f "$T/fake/tests/release/strip_marker.txt"
  [ "$state" = empty ] && : > "$T/fake/tests/release/strip_marker.txt"
  set +e; out="$(bash "$T/fake/tools/guard.sh" 2>&1)"; rc=$?; set -e
  [ "$rc" = 2 ] || { echo "FAIL: a $state strip_marker.txt gave exit $rc, want 2" >&2; exit 1; }
  case "$out" in *"missing or empty"*) ;; *) echo "FAIL: a $state marker file stopped without saying why: $out" >&2; exit 1 ;; esac
done
# The marker is read after the script has cd'd into its download folder (the "cd $DL" line), and $0 is the relative "tools/verify-release.sh" the release
# scripts call it by: the line must not depend on either (v1.4.160's first verify stopped on "missing or empty" because it used dirname of $0).
mkdir -p "$T/elsewhere"
line="$(grep '^STRIP_MARKER=' "$script")"
[ -n "$line" ] || { echo "FAIL: the STRIP_MARKER line is not in verify-release.sh" >&2; exit 1; }
got="$(cd "$T/elsewhere" && HERE="$(cd "$(dirname "$script")" && pwd)" bash -c 'here="$HERE"; '"$line"'; printf %s "$STRIP_MARKER"' tools/verify-release.sh)"
[ "$got" = "$marker" ] || { echo "FAIL: from another directory with a relative \$0 the marker read as '$got', want '$marker'" >&2; exit 1; }
n="$(grep -c 'unstripped ' "$script")"
[ "$n" -ge 2 ] || { echo "FAIL: unstripped is called from $n sites, want check_programs and the DMG" >&2; exit 1; }
echo "PASS verify_release_strip: the marker check refuses a mutant and is wired into the program and DMG checks"
