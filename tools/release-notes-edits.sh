#!/usr/bin/env bash
# notes_drop_edited TAG: reads a release's notes section on stdin and drops, for TAG, the lines tools/release-notes-edits.txt lists as removed from
# the published body (a deliberate correction after publication). Sourced by tools/verify-release.sh. RELEASE_NOTES_EDITS_FILE overrides the table (the tests).
_rne_file() { echo "${RELEASE_NOTES_EDITS_FILE:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-notes-edits.txt}"; }
notes_drop_edited() {
  local tag="$1" f prefixes=() p t line
  f="$(_rne_file)"
  if [ -f "$f" ]; then
    while IFS=$'\t' read -r t p; do
      case "$t" in ''|'#'*) continue ;; esac
      [ "$t" = "$tag" ] && [ -n "$p" ] && prefixes+=("$p")
    done < "$f"
  fi
  if [ "${#prefixes[@]}" = 0 ]; then cat; return; fi
  while IFS= read -r line || [ -n "$line" ]; do
    for p in "${prefixes[@]}"; do case "$line" in "$p"*) continue 2 ;; esac; done
    printf '%s\n' "$line"
  done
}
