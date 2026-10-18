#!/usr/bin/env bash
# tools/check-patch.sh: one command that runs every pre-post check an author must pass before
# posting a Go patch, so a census or drift failure shows up here instead of at DevOps's pre-push
# gate (#4825). It mirrors what the gate (tools/epoch-gate.sh) and the landing scan
# (tools/devops/lib.sh scan_patch) run. Commit your work first: the patch scan reads the commits.
#
#   tools/check-patch.sh           # from anywhere in the checkout
#
# It prints one PASS/FAIL line per step and exits non-zero if any step failed. Each step's exit
# status is checked on its own (never through a pipe, where the pipe's status would hide it).
set -euo pipefail

# Paths, resolved from this script's location so it runs from anywhere.
SELF=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SELF/.." && pwd)
GO_DIR="$REPO_ROOT/coral-go"
FE_DIR="$REPO_ROOT/tests/frontend"
GOFMT="$(go env GOROOT)/bin/gofmt" # the gofmt the toolchain ships, as the gate uses

# The DevOps scan (scan_patch): personal data and binaries, matching the landing scan exactly.
. "$REPO_ROOT/tools/devops/lib.sh"

# A short scratch dir (a long TMPDIR breaks go test and tmux on macOS), removed by its own path.
WORK=$(mktemp -d /tmp/check-patch.XXXX)
# Step logs live outside WORK so a failing step's full log survives for the author to read.
LOGS=$(mktemp -d /tmp/check-patch-logs.XXXX)
trap 'rm -rf -- "$WORK"' EXIT

FAILED=0

# run NAME CMD...: run one step with its output captured to a file (not a pipe, so the exit
# status checked is the command's own), print PASS/FAIL, and remember any failure. On failure
# it shows the lines that matter for a test run (--- FAIL / panic / a _test.go:line:), or the
# tail when none match, and names the full log, which is kept outside WORK.
run() {
  local name=$1
  shift
  local slug log
  slug=$(printf '%s' "$name" | tr -c 'A-Za-z0-9' _)
  log="$LOGS/$slug.log"
  if "$@" >"$log" 2>&1; then
    printf 'PASS  %s\n' "$name"
  else
    printf 'FAIL  %s\n' "$name"
    local sig
    sig=$(grep -nE '^(--- FAIL|FAIL|panic:|[[:space:]]+[^[:space:]]+_test\.go:[0-9]+:)' "$log" | head -40 || true)
    if [ -n "$sig" ]; then printf '%s\n' "$sig" | sed 's/^/    /'; else tail -n 40 "$log" | sed 's/^/    /'; fi
    printf '    full log: %s\n' "$log"
    FAILED=1
  fi
}

# --- the steps (each cds to an absolute dir first, so order never depends on the cwd) ---

check_gofmt() { # gofmt -l prints the files that need formatting and still exits 0, so fail on output
  cd "$GO_DIR"
  local out
  out=$("$GOFMT" -l .)
  [ -z "$out" ] || { printf 'these files are not gofmt-clean:\n%s\n' "$out"; return 1; }
}

vet_default() { cd "$GO_DIR"; go vet ./...; }
vet_dev() { cd "$GO_DIR"; go vet -tags 'dev fts5' ./...; }
# Windows as the Windows CI vets it: the pure-Go SQLite driver (CGO off, -tags fts5).
vet_windows() { cd "$GO_DIR"; CGO_ENABLED=0 GOOS=windows go vet -tags fts5 ./...; }

# The whole module runs, so the safego census and the route-query drift test (outside any one
# package) run too; -count=1 so a cached pass can't hide a new failure. Each go test gets its own
# short TMPDIR under WORK: an agent's own TMPDIR is often a long scratchpad path that breaks go
# test (file name too long) and the tmux tests, as the gate's mktemp -d /tmp/x.XXXX per run avoids.
# -timeout 30m: a package's default limit is 10 m, and internal/server/routes takes about that on a
# loaded box (609 s on epoch at nice 10, v1.4.96), so the default would fail it with no test at fault.
test_default() { cd "$GO_DIR"; mkdir -p "$WORK/go1"; TMPDIR="$WORK/go1" go test -count=1 -timeout 30m ./...; }
test_dev() { cd "$GO_DIR"; mkdir -p "$WORK/go2"; TMPDIR="$WORK/go2" go test -count=1 -timeout 30m -tags 'dev fts5' ./...; }

# The frontend node checks, lints and censuses the pre-push runs (npm run -s test:cloud): a Go-only
# patch can still break them, because they read the pkg/frontend sources. Fail with a one-line hint
# when the dependencies are not installed, rather than skipping silently.
test_cloud() {
  cd "$FE_DIR"
  if [ ! -x node_modules/.bin/eslint ]; then
    echo "tests/frontend/node_modules is missing; install it first: (cd tests/frontend && npm ci)"
    return 1
  fi
  mkdir -p "$WORK/fe"
  TMPDIR="$WORK/fe" npm run -s test:cloud
}

# Regenerate the per-route query allowlist and compare it to the committed table, without
# touching the tree (the generator writes to a scratch file). git diff --exit-code is the signal.
check_drift() {
  cd "$REPO_ROOT"
  python3 -I tools/gen_route_queries.py --out "$WORK/routes_query.go"
  git --no-pager diff --no-index --exit-code -- coral-go/pkg/tunnel/routes_query.go "$WORK/routes_query.go"
}

# Scan the whole range the post would carry (origin/main..HEAD, not only the tip), the way DevOps
# does: scan_patch (personal data, binaries) over every commit's diff, plus invisible characters in
# added lines, plus every commit's author and committer identity -- the operator's global git
# identity leaking into a commit is the personal-data failure we have actually had.
check_scan() {
  cd "$REPO_ROOT"
  local base rc=0
  base=$(git merge-base HEAD origin/main 2>/dev/null || echo HEAD~1)
  git format-patch --stdout "$base..HEAD" >"$WORK/range.patch"
  scan_patch "$WORK/range.patch" || rc=1
  local inv
  inv=$(perl -ne 'next unless /^\+/ && !/^\+\+\+/;
    print "$.: $_" if /[\x{00a0}\x{061c}\x{115f}\x{1160}\x{180e}\x{200b}-\x{200f}\x{2028}\x{2029}\x{202a}-\x{202e}\x{2060}-\x{2064}\x{2066}-\x{206f}\x{3164}\x{feff}\x{ffa0}]/' \
    "$WORK/range.patch") || true
  [ -z "$inv" ] || { printf 'invisible characters in added lines:\n%s\n' "$inv"; rc=1; }
  # Every author and committer in the range must be a role address (@example.invalid or @example.test).
  local who
  who=$(git log --format='%H %ae %ce' "$base..HEAD" | awk '$2 !~ /@example\.(invalid|test)$/ || $3 !~ /@example\.(invalid|test)$/ { print }' || true)
  [ -z "$who" ] || { printf 'commits whose author or committer is not a role address:\n%s\n' "$who"; rc=1; }
  return "$rc"
}

STEPS=(
  "gofmt -l (whole module)|check_gofmt"
  "go vet (default tags)|vet_default"
  "go vet (dev fts5 tags)|vet_dev"
  "go vet (GOOS=windows, fts5, CGO off)|vet_windows"
  "go test -count=1 -timeout 30m ./... (default tags)|test_default"
  "go test -count=1 -timeout 30m ./... (dev fts5 tags)|test_dev"
  "npm run test:cloud (frontend node checks)|test_cloud"
  "route-query table drift (gen_route_queries.py)|check_drift"
  "patch scan of origin/main..HEAD (personal data, binaries, invisibles, identity)|check_scan"
)

for spec in "${STEPS[@]}"; do
  run "${spec%%|*}" "${spec#*|}"
done

echo
if [ "$FAILED" -ne 0 ]; then
  echo "check-patch: FAIL — fix the steps above before posting (full logs under $LOGS)."
  exit 1
fi
rm -rf -- "$LOGS"
echo "check-patch: all checks passed."
