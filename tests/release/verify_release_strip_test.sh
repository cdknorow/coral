#!/usr/bin/env bash
# verify-release.sh refuses a coral binary that holds the dashboard's source comments (built without the strip-static overlay, #6208).
# The test runs the script's own helper on a stripped binary and on a mutant that carries the marker, and checks every call site is wired.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
script="$here/../../tools/verify-release.sh"
T="$(mktemp -d /tmp/vrstrip.XXXXXX)"
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/tools"
sed -n '/^STRIP_MARKER=/,/^}/p' "$script" | sed 's|\$0|"$SCRIPT"|' > "$T/helper.sh"
[ -s "$T/helper.sh" ] || { echo "FAIL: helper not found in verify-release.sh" >&2; exit 1; }
marker="$(head -n1 "$here/strip_marker.txt")"
printf 'ELF....clean binary....' > "$T/stripped"
printf 'ELF....%s....' "$marker" > "$T/mutant"
SCRIPT="$script"
# shellcheck disable=SC1090
. "$T/helper.sh"
unstripped "$T/stripped" && { echo "FAIL: a stripped binary was refused" >&2; exit 1; }
unstripped "$T/mutant" || { echo "FAIL: a binary holding the source comment was accepted" >&2; exit 1; }
# the marker file is read, so an empty one cannot make every binary pass silently
[ -n "$STRIP_MARKER" ] || { echo "FAIL: marker not read" >&2; exit 1; }
n="$(grep -c 'unstripped ' "$script")"
[ "$n" -ge 2 ] || { echo "FAIL: unstripped is called from $n sites, want check_programs and the DMG" >&2; exit 1; }
echo "PASS verify_release_strip: the marker check refuses a mutant and is wired into the program and DMG checks"
