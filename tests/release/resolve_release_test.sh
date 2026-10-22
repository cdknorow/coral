#!/usr/bin/env bash
# resolve_release_test.sh: the tier table release.yml used before #3855 moved it into
# resolve_release.sh, so the move changes no tag's build tags or prerelease flag.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
fail=0
check() { # tag build_tags prerelease
  out=$(bash "$here/resolve_release.sh" "$1")
  got_tags=$(sed -n 's/^build_tags=//p' <<<"$out"); got_pre=$(sed -n 's/^prerelease=//p' <<<"$out"); got_ver=$(sed -n 's/^version=//p' <<<"$out")
  if [ "$got_tags" != "$2" ] || [ "$got_pre" != "$3" ] || [ "$got_ver" != "${1#v}" ]; then
    echo "FAIL $1: build_tags=$got_tags prerelease=$got_pre version=$got_ver (want $2 $3 ${1#v})"; fail=1
  fi
}
check v1.4.69 fts5 false
check v1.4.69-rc1 fts5 true
check v1.4.69-rc.2 fts5 true
check v1.4.69-dev fts5,dev true
check v1.4.69-beta fts5,beta true
check v1.4.69-forDropbox fts5,dropboxers true
check v1.4.69-dev-forDropbox fts5,dropboxers true   # -forDropbox wins, as the old if/elif did
check v1.4.69-beta-dev fts5,beta true               # -beta before -dev
out=$(bash "$here/resolve_release.sh" v1.4.69)
[ "$(sed -n 's/^commands=//p' <<<"$out")" = "coral launch-coral coral-board coral-agent coral-hook-agentic-state coral-hook-message-check coral-hook-session-start coral-hook-task-sync coral-update" ] || { echo "FAIL commands list changed"; fail=1; }
[ "$(sed -n 's/^gui_commands=//p' <<<"$out")" = "coral-tray coral-app" ] || { echo "FAIL gui list changed"; fail=1; }
[ "$fail" = 0 ] && echo "PASS resolve_release: 8 tags, both program lists"
exit "$fail"
