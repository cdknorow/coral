#!/usr/bin/env bash
# tools/check-repo-move.sh (#5525): the rules it applies (release tag shape, the update check's redirect rule, cask parsing) hold on fixtures, and the script stays READ-ONLY:
# no request that changes anything on GitHub, no push, tag, dispatch, install or tap, nothing written outside its own temp folder. No network.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
script="$root/tools/check-repo-move.sh"
fail=0
ok() { [ "$2" = "$3" ] && echo "PASS $1" || { echo "FAIL $1 (got '$2', want '$3')"; fail=1; }; }

out=$(bash "$script" --self-test 2>&1) || true
ok "the self-test holds its rules" "$(echo "$out" | tail -1)" "self-test: all rules hold"

# read-only: none of these may appear as a command in the script (comments are dropped first)
code=$(grep -vE '^[[:space:]]*(#|row |case )' "$script")
for pat in 'git push' 'git tag ' 'git commit' 'git remote add' 'git config --global' 'gh release (create|edit|delete|upload)' 'gh workflow run' 'gh repo (create|edit|delete|rename|transfer)' \
           'gh api[^|]* (-X|--method)' 'curl[^|]* (-X|--request) *(POST|PUT|PATCH|DELETE)' 'curl[^|]* (-d|--data|-F|--form|-T|--upload-file)[ =]' '(^|[=(| ])brew (install|tap|untap|upgrade|uninstall)' 'rm -rf *(~|\$HOME|/)'; do
  ok "no '$pat'" "$(printf '%s\n' "$code" | grep -Ec "$pat" || true)" 0
done
# curl runs with -q (no ~/.curlrc, no ~/.netrc) and git with an empty environment, so no credential of the operator's is picked up
ok "every curl call disables the config files" "$(printf '%s\n' "$code" | grep -E '(^|[=(| ])curl ' | grep -v 'command -v' | grep -vc -- ' -q ' || true)" 0
ok "every git call that reaches the network clears the environment" "$(printf '%s\n' "$code" | grep -E '(^|[=(| ])git (ls-remote|clone)' | grep -vc 'env -i' || true)" 0
# the temp folder is removed only by a guarded path
ok "the cleanup removes only its own folder" "$(printf '%s\n' "$code" | grep -c 'case "$TMP" in /tmp/check-repo-move.\*) rm -rf "$TMP"')" 1
exit $fail
