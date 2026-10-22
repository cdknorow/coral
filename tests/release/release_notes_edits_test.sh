#!/usr/bin/env bash
# tools/release-notes-edits.sh: lines deliberately removed from a published body are dropped from the tag's notes before they are compared. No network.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # nothing here runs Coral, but the telemetry census wants it on every script that names the repository
root="$(cd "$(dirname "$0")/../.." && pwd)"
. "$root/tools/release-notes-edits.sh"
fail=0
ok() { [ "$2" = "$3" ] && echo "PASS $1" || { echo "FAIL $1 (got '$2', want '$3')"; fail=1; }; }
T="$(mktemp /tmp/rne.XXXX)"; case "$T" in /tmp/rne.????) ;; *) echo "bad temp file"; exit 1 ;; esac
trap 'rm -f "$T"' EXIT
printf '# comment\nv1.4.142\t- **Gone:**\nv1.4.142\t- Also gone\nv9.9.9\t- Other tag line\n' > "$T"
export RELEASE_NOTES_EDITS_FILE="$T"
in=$'- keep one\n- **Gone:** the text\n- keep two\n- Also gone too\n- **Gone not at start:** x\n'
ok "the listed lines of the tag are dropped" "$(notes_drop_edited v1.4.142 <<<"$in")" "$(printf '%s\n' '- keep one' '- keep two' '- **Gone not at start:** x')"
ok "another tag's rows do not apply" "$(notes_drop_edited v1.4.143 <<<"$in")" "$(printf '%s' "$in")"
ok "a row of another tag does not drop here" "$(notes_drop_edited v1.4.142 <<<"- Other tag line")" "- Other tag line"
# a three-column row replaces the line (the published body has the corrected line in the same place)
printf 'v1.4.150\t- **Replace me:**\t- **Replace me:** the corrected text\nv1.4.150\t- Drop me\n' > "$T"
export RELEASE_NOTES_EDITS_FILE="$T"
in2=$'- keep\n- **Replace me:** the old text\n- Drop me too\n- keep last\n'
ok "a replaced row swaps the line and a drop row drops it" "$(notes_drop_edited v1.4.150 <<<"$in2")" "$(printf '%s\n' '- keep' '- **Replace me:** the corrected text' '- keep last')"
ok "a replaced row of another tag does not apply" "$(notes_drop_edited v1.4.151 <<<"$in2")" "$(printf '%s' "$in2")"
export RELEASE_NOTES_EDITS_FILE=/nonexistent
ok "no table: everything is kept" "$(notes_drop_edited v1.4.142 <<<"$in")" "$(printf '%s' "$in")"
unset RELEASE_NOTES_EDITS_FILE
ok "the shipped table drops the v1.4.142 Team settings line" "$(notes_drop_edited v1.4.142 <<<'- **The Team settings tab strip no longer changes after it appears:** x' | wc -l | tr -d ' ')" "0"
ok "the shipped table does not touch v1.4.143" "$(notes_drop_edited v1.4.143 <<<'- **The Team settings tab strip no longer changes after it appears:** x' | wc -l | tr -d ' ')" "1"
exit "$fail"
