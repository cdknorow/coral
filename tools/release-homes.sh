#!/usr/bin/env bash
# Sourced by tools/verify-release.sh, tools/devops/*.sh and tests/release/smoke_macos.sh (#5299).
# release_home_current             -> OWNER/REPO of the current home (the last line of tools/release-homes.txt)
# release_home_for_tag vX.Y.Z      -> OWNER/REPO that built and signed that tag
# release_tap_repo [OWNER/REPO]    -> OWNER/homebrew-coral for that home (default: the current one)
# RELEASE_HOMES_FILE overrides the table (the test uses it).
_rh_file() { echo "${RELEASE_HOMES_FILE:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-homes.txt}"; }
_rh_rows() { grep -vE '^[[:space:]]*(#|$)' "$(_rh_file)"; }
release_home_current() { _rh_rows | tail -1 | awk '{print $2}'; }
release_home_for_tag() {
  local tag="${1#v}" first repo best=""
  while read -r first repo; do
    first="${first#v}"
    # first <= tag, as versions
    if [ "$(printf '%s\n%s\n' "$first" "$tag" | sort -V | head -1)" = "$first" ]; then best="$repo"; fi
  done < <(_rh_rows)
  [ -n "$best" ] && echo "$best"
}
release_tap_repo() { local r="${1:-$(release_home_current)}"; echo "${r%%/*}/homebrew-coral"; }
# release_has_stable_names VERSION  -> success when that release is expected to carry the stable-name copies (#5333), from 1.4.130
# on: an older release is verified without them. A suffix (-rc1, -dev) is ignored for the comparison.
release_has_stable_names() {
  local v="${1#v}"; v="${v%%-*}"
  [ "$(printf '%s\n%s\n' 1.4.130 "$v" | sort -V | head -1)" = 1.4.130 ]
}
