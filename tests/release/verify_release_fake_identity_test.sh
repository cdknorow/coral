#!/usr/bin/env bash
# verify-release.sh refuses a program that holds the updater rehearsal's fake identity (internal/updater/testrig, #6290), and the check is wired into the program check.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
script="$here/../../tools/verify-release.sh"
T="$(mktemp -d /tmp/vrfake.XXXXXX)"
trap 'rm -rf "$T"' EXIT
sed -n '/^FAKE_IDENTITY_MARKER=/,/^}/p' "$script" > "$T/helper.sh"
[ -s "$T/helper.sh" ] || { echo "FAIL: helper not found in verify-release.sh" >&2; exit 1; }
# shellcheck disable=SC1090
. "$T/helper.sh"
printf 'ELF....clean binary....' > "$T/clean"
printf 'ELF....https://rehearsal.invalid/coral-promote....' > "$T/mutant"
holds_fake_identity "$T/clean" && { echo "FAIL: a clean program was refused" >&2; exit 1; }
holds_fake_identity "$T/mutant" || { echo "FAIL: a program holding the fake identity was accepted" >&2; exit 1; }
# the marker is the one the rig uses, and the check sits in check_programs
grep -rqF 'rehearsal.invalid' "$here/../../coral-go/internal/updater/testrig/release.go" || { echo "FAIL: the testrig no longer uses the marker: update the check" >&2; exit 1; }
sed -n '/^check_programs()/,/^}/p' "$script" | grep -q 'holds_fake_identity "\$f"' || { echo "FAIL: check_programs does not call holds_fake_identity" >&2; exit 1; }
echo "PASS verify_release_fake_identity: the check refuses a mutant and is wired into the program check"
