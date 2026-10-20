#!/usr/bin/env bash
# tools/devops/pre-push-hook.sh (#5444): through a real git push to a throwaway bare remote, a branch that adds a program is refused
# and nothing reaches the remote, a normal branch goes through, and a branch whose later commit removes the program is still refused.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
T=$(mktemp -d /tmp/pph.XXXX); case "$T" in /tmp/pph.????) ;; *) echo "bad temp dir"; exit 1;; esac
trap 'rm -rf "$T"' EXIT
git init -q --bare "$T/remote.git"
git init -q "$T/w"; cd "$T/w"; git config user.name t; git config user.email t@example.invalid
mkdir -p tools/devops; cp "$root/tools/devops/check_binaries.py" "$root/tools/devops/binary-allowlist.txt" "$root/tools/devops/pre-push-hook.sh" "$root/tools/devops/install-pre-push-hook.sh" tools/devops/
git remote add origin "$T/remote.git"; echo base > a.txt; git add -A; git commit -qm base; git branch -M main; git push -q origin main
git fetch -q origin
bash tools/devops/install-pre-push-hook.sh >/dev/null
fail=0
check() { [ "$2" = "$3" ] && echo "PASS $1" || { echo "FAIL $1 (got $2, want $3)"; fail=1; }; }
git checkout -q -b good; echo x > b.txt; git add b.txt; git commit -qm good
rc=0; git push -q origin good >/dev/null 2>&1 || rc=$?; check "a normal branch is pushed" "$rc" 0
git checkout -q -b bad main; printf '\x7fELF\x02' > routes.test; git add routes.test; git commit -qm elf
rc=0; git push -q origin bad >/dev/null 2>&1 || rc=$?; check "a branch adding a program is refused" "$rc" 1
check "and nothing reached the remote" "$(git ls-remote origin refs/heads/bad | wc -l | tr -d ' ')" 0
git rm -q routes.test; git commit -qm rm
rc=0; git push -q origin bad >/dev/null 2>&1 || rc=$?; check "removing it in a later commit does not help (the history is pushed)" "$rc" 1
git push -q origin :refs/heads/good >/dev/null 2>&1; check "a deletion is not checked" "$?" 0
# a branch with no common history with anything on the remote has no merge base: it is checked from its own commits (#5444 review)
git checkout -q --orphan solo; git rm -rfq . ; printf '\x7fELF\x02' > prog; git add prog; git commit -qm solo
rc=0; git push -q origin solo >/dev/null 2>&1 || rc=$?; check "an orphan branch adding a program is refused" "$rc" 1
check "and nothing reached the remote" "$(git ls-remote origin refs/heads/solo | wc -l | tr -d ' ')" 0
# the checker is not read from the working tree: with the folder emptied (the orphan branch above has no tools/) the check still ran, as the refusal shows
ls tools >/dev/null 2>&1 && { echo "FAIL the working tree still has tools/"; fail=1; } || echo "PASS the working tree had no tools/ and the push was still checked"
git rm -q prog; echo ok > fine.txt; git add fine.txt; git commit -qm fine --amend
rc=0; git push -q origin solo >/dev/null 2>&1 || rc=$?; check "an orphan branch of plain files is pushed" "$rc" 0
# without the installed checker the push is refused, not let through
mv "$(git rev-parse --git-common-dir)/hooks/coral-5444" "$T/moved"
git checkout -q -b plain main; echo y > c.txt; git add c.txt; git commit -qm plain
rc=0; git push -q origin plain >/dev/null 2>&1 || rc=$?; check "a missing checker refuses the push" "$rc" 1
mv "$T/moved" "$(git rev-parse --git-common-dir)/hooks/coral-5444"
rc=0; git push -q origin plain >/dev/null 2>&1 || rc=$?; check "and with it back the same push goes through" "$rc" 0
exit "$fail"
