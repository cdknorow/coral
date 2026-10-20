#!/usr/bin/env bash
# The per-tag release home (tools/release-homes.sh, #5299): a tag is checked against the repository that built it.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
. "$here/../../tools/release-homes.sh"
fail=0
eq() { if [ "$2" = "$3" ]; then echo "ok: $1"; else echo "FAIL: $1: got [$2] want [$3]" >&2; fail=1; fi; }

# the shipped table: one home, so every tag is checked against it
eq "shipped table: current home" "$(release_home_current)" "cdknorow/coral"
eq "shipped table: v1.4.130" "$(release_home_for_tag v1.4.130)" "cdknorow/coral"
eq "shipped table: tap" "$(release_tap_repo)" "cdknorow/homebrew-coral"

# a table with a move at v1.4.140
t="$(mktemp)"; trap 'rm -f "$t"' EXIT
printf '# comment\n\nv0.0.0 cdknorow/coral\nv1.4.140 subgentic/coral\n' > "$t"
export RELEASE_HOMES_FILE="$t"
eq "before the move" "$(release_home_for_tag v1.4.139)" "cdknorow/coral"
eq "the first tag of the new home" "$(release_home_for_tag v1.4.140)" "subgentic/coral"
eq "after the move" "$(release_home_for_tag v1.4.141)" "subgentic/coral"
eq "a numeric, not a lexical, comparison (1.4.9 < 1.4.140)" "$(release_home_for_tag v1.4.9)" "cdknorow/coral"
eq "a numeric comparison upward (1.10.0 > 1.4.140)" "$(release_home_for_tag v1.10.0)" "subgentic/coral"
eq "current home" "$(release_home_current)" "subgentic/coral"
eq "tap follows the current home" "$(release_tap_repo)" "subgentic/homebrew-coral"
eq "tap of an older home" "$(release_tap_repo cdknorow/coral)" "cdknorow/homebrew-coral"

# the stable-name copies are expected from 1.4.130 on only (#5333)
for v in v1.4.129 1.4.9 v1.3.200 v0.9.0; do if release_has_stable_names "$v"; then eq "no stable names expected for $v" yes no; else eq "no stable names expected for $v" no no; fi; done
for v in v1.4.130 1.4.131 v1.4.130-rc1 v1.5.0 v2.0.0 1.10.0; do if release_has_stable_names "$v"; then eq "stable names expected for $v" yes yes; else eq "stable names expected for $v" no yes; fi; done
exit "$fail"
