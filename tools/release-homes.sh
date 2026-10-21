#!/usr/bin/env bash
# Sourced by tools/verify-release.sh, tools/devops/*.sh and tests/release/smoke_macos.sh (#5299).
# release_home_current             -> OWNER/REPO of the current home (the last line of tools/release-homes.txt)
# release_home_for_tag vX.Y.Z      -> OWNER/REPO that built and signed that tag
# release_tap_repo [OWNER/REPO]    -> the Homebrew tap repository: RELEASE_TAP_REPO for the current home; for an older home, that home's own OWNER/homebrew-coral
# release_tap_name                 -> the name brew uses for that tap (OWNER/coral), for "brew install --cask OWNER/coral/coral"
# RELEASE_HOMES_FILE overrides the table and RELEASE_TAP_REPO the tap (the tests use them).
#
# THE ONE PLACE THE TAP IS NAMED. The tap does not follow the release home: it stays cdknorow/homebrew-coral while an enterprise setting
# blocks deploy keys in the subgentic organisation (#5530). When the tap moves, change this one line (and the TAP_URL lines of the
# update-tap job in release.yml, and the brew lines of the docs, which the post for #5530 lists).
: "${RELEASE_TAP_REPO:=cdknorow/homebrew-coral}"
# The folder is read once, when this file is sourced: a caller that changes directory later (verify-release.sh does, into its work
# folder) must still find the table; a lookup at call time with a relative $0 failed and made the tap name come out wrong (v1.4.142).
_RH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_rh_file() { echo "${RELEASE_HOMES_FILE:-$_RH_DIR/release-homes.txt}"; }
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
release_tap_repo() {
  local r="${1:-$(release_home_current)}"
  if [ "$r" = "$(release_home_current)" ]; then echo "$RELEASE_TAP_REPO"; else echo "${r%%/*}/homebrew-coral"; fi
}
release_tap_name() { local t; t="$(release_tap_repo "$@")"; echo "${t%%/*}/${t#*/homebrew-}"; }
# release_has_stable_names VERSION  -> success when that release is expected to carry the stable-name copies (#5333), from 1.4.130
# on: an older release is verified without them. A suffix (-rc1, -dev) is ignored for the comparison.
release_has_stable_names() {
  local v="${1#v}"; v="${v%%-*}"
  [ "$(printf '%s\n%s\n' 1.4.130 "$v" | sort -V | head -1)" = 1.4.130 ]
}
