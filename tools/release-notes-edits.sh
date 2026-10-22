#!/usr/bin/env bash
# notes_drop_edited TAG: reads a release's notes section on stdin and, for TAG, applies the corrections tools/release-notes-edits.txt lists for the
# published body (a deliberate correction after publication): a row of two columns DROPS every line that starts with the text; a row of three columns
# REPLACES it with the third column, the line as the published body now has it. Sourced by tools/verify-release.sh. RELEASE_NOTES_EDITS_FILE overrides
# the table (the tests).
_rne_file() { echo "${RELEASE_NOTES_EDITS_FILE:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/release-notes-edits.txt}"; }
notes_drop_edited() {
  local tag="$1" f prefixes=() repl=() p t r line i
  f="$(_rne_file)"
  if [ -f "$f" ]; then
    while IFS=$'\t' read -r t p r; do
      case "$t" in ''|'#'*) continue ;; esac
      [ "$t" = "$tag" ] && [ -n "$p" ] && { prefixes+=("$p"); repl+=("$r"); }
    done < "$f"
  fi
  if [ "${#prefixes[@]}" = 0 ]; then cat; return; fi
  while IFS= read -r line || [ -n "$line" ]; do
    for i in "${!prefixes[@]}"; do
      case "$line" in
        "${prefixes[$i]}"*) [ -n "${repl[$i]}" ] && printf '%s\n' "${repl[$i]}"; continue 2 ;;
      esac
    done
    printf '%s\n' "$line"
  done
}
