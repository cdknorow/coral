#!/usr/bin/env bash
# tools/devops/check_binaries.py (#5444): a range that adds a program or a file over 5 MB is refused; text, a small data file, a PNG
# and an allowlisted path are not. Runs in a throwaway repository under a short /tmp path.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
chk="$root/tools/devops/check_binaries.py"
T=$(mktemp -d /tmp/cbt.XXXX); case "$T" in /tmp/cbt.????) ;; *) echo "bad temp dir"; exit 1;; esac
trap 'rm -rf "$T"' EXIT
cd "$T"; git init -q .; git config user.name t; git config user.email t@example.invalid
echo base > a.txt; git add a.txt; git commit -qm base; base=$(git rev-parse HEAD)
fail=0
expect() { # name want(0|1) ; runs the check over base..HEAD
  local got=0; python3 -I "$chk" "$base" HEAD >/dev/null 2>&1 || got=1
  [ "$got" = "$2" ] && echo "PASS $1" || { echo "FAIL $1 (exit $got, want $2)"; fail=1; }
  git reset -q --hard "$base"
}
echo text > b.txt; printf '\x89PNG\r\n' > i.png; head -c 100000 /dev/zero > data.bin; git add -A; git commit -qm ok
expect "text, a png header and a zero-filled data file pass" 0
printf '\x7fELF\x02\x01\x01' > routes.test; git add -A; git commit -qm elf
expect "an ELF program is refused" 1
printf '\xcf\xfa\xed\xfe\x07\x00' > tool; git add -A; git commit -qm macho
expect "a Mach-O program is refused" 1
printf '\xca\xfe\xba\xbe\x00' > fat; git add -A; git commit -qm fat
expect "a universal Mach-O program is refused" 1
printf 'MZ\x90\x00' > tool.exe; git add -A; git commit -qm pe
expect "a PE program is refused" 1
head -c 5300000 /dev/zero > big.dat; git add -A; git commit -qm big
expect "a file over 5 MB is refused" 1
printf '\x7fELF\x02' > ok.bin; git add -A; git commit -qm allowed
printf '# test list\nok.bin\n' > "$T.allow"
got=0; CORAL_BINARY_ALLOWLIST="$T.allow" python3 -I "$chk" "$base" HEAD >/dev/null 2>&1 || got=1
[ "$got" = 0 ] && echo "PASS an allowlisted path passes" || { echo "FAIL an allowlisted path"; fail=1; }
rm -f "$T.allow"
git reset -q --hard "$base"
echo q > a.txt; git commit -qam edit
expect "a modified text file passes" 0
# the history is what is pushed, not the net diff
printf '\x7fELF' > p; git add p; git commit -qm addp; git rm -q p; git commit -qm rmp
expect "a program added in one commit and removed in the next is still refused (it would be pushed with the history)" 1
exit "$fail"
