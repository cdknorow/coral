#!/usr/bin/env bash
# The real upgrade from the previous release (#5325), on a macOS runner: the previous release's app is installed in a temp folder and the new
# release's disk image goes through coral-update's macOS apply steps with the REAL tools: it is mounted read-only, the app is copied out beside
# the install, and codesign (--deep --strict), spctl, the pinned Team ID, the stapled notarization ticket and the bundle version are all checked
# on the copy; then the atomic swap and the rollback. The app is not started (a CI runner has no login session for it).
#
# usage: tests/release/upgrade_macos.sh <previous Coral.dmg> <new Coral.dmg> <new version, e.g. 1.4.131> <Developer ID Team ID>
# The two images are the release's own, from the GitHub release (SHA256SUMS.txt checks them first, as the updater does).
set -euo pipefail

prev_dmg=${1:?previous Coral dmg}
new_dmg=${2:?new Coral dmg}
version=${3:?new version}
team=${4:?Developer ID Team ID}

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d /tmp/coral-upgrade.XXXXXX)
mnt="$work/prev-mnt"
cleanup() {
  hdiutil detach -force "$mnt" >/dev/null 2>&1 || true
  rm -rf "${work:?}"
}
trap cleanup EXIT

mkdir "$mnt"
hdiutil attach -nobrowse -readonly -noautoopen -mountpoint "$mnt" "$prev_dmg" >/dev/null
test -d "$mnt/Coral.app"
ditto --noqtn "$mnt/Coral.app" "$work/Coral-prev.app"
hdiutil detach -force "$mnt" >/dev/null

cd "$here/../../coral-go"
CORAL_APPLY_OLD_APP="$work/Coral-prev.app" \
CORAL_APPLY_NEW_DMG="$new_dmg" \
CORAL_APPLY_NEW_VERSION="$version" \
CORAL_APPLY_TEAM_ID="$team" \
  go test ./internal/updater/apply -run 'TestRealUpgradeFromThePreviousRelease' -count=1 -v
