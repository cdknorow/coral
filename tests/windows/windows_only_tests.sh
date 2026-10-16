#!/usr/bin/env bash
# windows_only_tests.sh [--check-routes REGEX]: run in coral-go/. Prints, one per line, every package that has a test
# file Go builds for windows but not for linux (a _windows_test.go, or a test file whose build constraint needs windows),
# so windows-tests.yml runs them all and a new Windows test can't be skipped by a hand-kept list. internal/server/routes
# is left out (the workflow runs it with a -run filter, it is too slow whole); with --check-routes REGEX, the script
# instead fails unless every Test function in routes' Windows-only test files matches REGEX.
set -euo pipefail
list() { GOOS=$1 go list -tags fts5 -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' ./... | tr '\\' / | grep . | sort -u; }   # forward slashes on Windows too
win=$(list windows); lin=$(list linux)
only=$(comm -23 <(printf '%s\n' "$win") <(printf '%s\n' "$lin"))
[ -n "$only" ] || { echo "windows_only_tests: found no Windows-only test files (go list failed?)" >&2; exit 1; }
root=$(go list -m -f '{{.Dir}}' | tr '\\' /)
if [ "${1:-}" = "--check-routes" ]; then
  re=${2:?usage: --check-routes REGEX}; bad=0; n=0
  while IFS= read -r f; do
    case "$f" in "$root/internal/server/routes/"*) ;; *) continue ;; esac
    while IFS= read -r t; do
      n=$((n + 1))
      printf '%s\n' "$t" | grep -qE "$re" || { echo "::error::$t (${f#"$root/"}) is a Windows-only routes test the routes filter does not run" >&2; bad=1; }
    done < <(grep -oE '^func (Test[A-Za-z0-9_]+)' "$f" | sed 's/^func //')
  done <<<"$only"
  [ $bad -eq 0 ] || exit 1
  echo "windows_only_tests: all $n Windows-only routes tests match the routes filter"
  exit 0
fi
if [ "${1:-}" = "--tests" ]; then
  # One line per package: "./pkg<TAB>Test1|Test2" (the Test functions in its Windows-only test files; empty when they
  # hold only helpers), so a package outside the hand-kept list can run just its Windows-only tests.
  while IFS= read -r f; do
    d=$(dirname "$f"); rel=${d#"$root"}; [ "$rel" = "/internal/server/routes" ] && continue
    names=$(grep -oE '^func (Test[A-Za-z0-9_]+)' "$f" | sed 's/^func //' | paste -sd'|' - || true)
    printf './%s\t%s\n' "${rel#/}" "$names"
  done <<<"$only" | awk -F'\t' '{ if (!($1 in t)) { o[++n] = $1; t[$1] = $2 } else if ($2 != "") t[$1] = (t[$1] == "" ? $2 : t[$1] "|" $2) }
                                END { for (i = 1; i <= n; i++) printf "%s\t%s\n", o[i], t[o[i]] }'
  exit 0
fi
while IFS= read -r f; do
  d=$(dirname "$f"); rel=${d#"$root"}
  [ "$rel" = "/internal/server/routes" ] && continue
  echo "./${rel#/}"
done <<<"$only" | sort -u
