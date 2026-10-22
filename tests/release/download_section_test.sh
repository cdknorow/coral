#!/usr/bin/env bash
# tools/devops/download-section.sh (#5467): the Download section of a release's notes names the tag's five stable files (no Homebrew
# line since 144), follows the release home of the table (a moved repository), and refuses a release before 1.4.130. No network.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
fail=0
ok() { [ "$2" = "$3" ] && echo "PASS $1" || { echo "FAIL $1 (got '$2', want '$3')"; fail=1; }; }
out=$(bash "$root/tools/devops/download-section.sh" v1.4.133)
ok "starts with the Download heading" "$(echo "$out" | sed -n 1p)" "### Download"
for f in Coral.dmg Coral-x64.msi Coral-x64-portable.zip coral-linux-amd64.tar.gz coral-linux-arm64.tar.gz; do
  ok "links $f of the tag" "$(echo "$out" | grep -c "https://github.com/cdknorow/coral/releases/download/v1.4.133/$f)")" 1
done
ok "no Homebrew line" "$(echo "$out" | grep -ci 'brew')" 0
ok "an order: macOS first" "$(echo "$out" | sed -n 3p | grep -o '^\[\*\*macOS (.dmg)\*\*\]')" '[**macOS (.dmg)**]'
H=$(mktemp /tmp/dls.XXXX); case "$H" in /tmp/dls.????) ;; *) echo "bad temp file"; exit 1;; esac
trap 'rm -f "$H"' EXIT
printf 'v0.0.0 cdknorow/coral\nv1.5.0 neworg/coral\n' > "$H"
moved=$(RELEASE_HOMES_FILE="$H" bash "$root/tools/devops/download-section.sh" v1.5.1)
ok "a release after a move names the new home" "$(echo "$moved" | grep -c 'https://github.com/neworg/coral/releases/download/v1.5.1/Coral.dmg')" 1
ok "and still no Homebrew line" "$(echo "$moved" | grep -ci 'brew')" 0
old=$(RELEASE_HOMES_FILE="$H" bash "$root/tools/devops/download-section.sh" v1.4.133)
ok "a release before the move keeps the old home" "$(echo "$old" | grep -c 'https://github.com/cdknorow/coral/releases/download/v1.4.133/Coral.dmg')" 1
# the shipped table (#5530): v1.4.141 still names cdknorow, v1.4.142 the new home, and both keep the one tap
for v in 1.4.141:cdknorow 1.4.142:subgentic 1.4.150:subgentic; do
  t=${v%%:*}; o=${v##*:}; s=$(bash "$root/tools/devops/download-section.sh" "v$t")
  ok "shipped table: v$t links name $o" "$(echo "$s" | grep -c "https://github.com/$o/coral/releases/download/v$t/Coral.dmg")" 1
  ok "shipped table: v$t has no Homebrew line" "$(echo "$s" | grep -ci 'brew')" 0
  ok "shipped table: v$t names no other owner" "$(echo "$s" | grep -c "github.com/$([ "$o" = cdknorow ] && echo subgentic || echo cdknorow)/")" 0
done
rc=0; bash "$root/tools/devops/download-section.sh" v1.4.129 >/dev/null 2>&1 || rc=$?
ok "a release before 1.4.130 is refused" "$rc" 1
for bad in 'v1.9.0)[x](http://evil.example/a' 'v1.4.200 x' 'v1.4.200-$(id)' 'v1.4.200
' 'v1.4' ''; do
  rc=0; out=$(bash "$root/tools/devops/download-section.sh" "$bad" 2>/dev/null) || rc=$?
  ok "a version that is not a version is refused ($(printf %q "$bad"))" "$rc:$out" "1:"
done
ok "a suffix is still a version" "$(bash "$root/tools/devops/download-section.sh" v1.4.200-rc1 | grep -c 'download/v1.4.200-rc1/Coral.dmg')" 1
# The Coral For Dropbox tier ships no MSI: its notes link no installer, and the other four links stay
d=$(bash "$root/tools/devops/download-section.sh" v1.4.200-forDropbox)
ok "a forDropbox release links no MSI" "$(echo "$d" | grep -c 'msi')" 0
ok "a forDropbox release still links the portable zip" "$(echo "$d" | grep -c 'Coral-x64-portable.zip')" 1
ok "a forDropbox release keeps the dmg link first, then the zip" "$(echo "$d" | sed -n 3p | grep -c '^\[\*\*macOS (.dmg)\*\*\](.*Coral.dmg) &bull; \[Windows portable')" 1
ok "a prod release still links the MSI" "$(bash "$root/tools/devops/download-section.sh" v1.4.200 | grep -c 'Coral-x64.msi')" 1
exit "$fail"
