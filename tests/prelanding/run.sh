#!/usr/bin/env bash
# tests/prelanding/run.sh: the team's full pre-land check of one commit of this repo, as DevOps runs it:
# gofmt, vet, go test, go test -tags 'dev fts5', test:cloud, and every CDP suite (in the runner's
# loopback-only network namespace on Linux), at nice 10.
#
#   run.sh --message <board message id>   what epoch's "Epoch Test Runner" agent runs (#3487)
#   run.sh <ref> [id]                     by hand
#
# With --message the script reads that board message itself (coral-board read --id --json) and acts
# only when its sender is the Operator (or in PRELAND_REQUESTERS) and it has a line
# "PRELAND-CHECK ref=<ref> id=<id>", so no text from a message ever reaches a command line.
# The agent's CLI permissions allow only this script with --message (see the #3487 design).
#
# It only checks code that is already on a branch or tag of origin: it refuses a pull request ref, any
# commit that no branch or tag of origin contains, and malformed refs and ids. The ref is resolved to a
# commit once; that commit is what is checked out and reported, so a branch moving mid-run can't mix
# code.
#
# Output: the commit, pass/fail lines (package, test and suite names) and the log id, and nothing else
# (no host paths), so it can be posted as is. Each step's full output stays in $PRELAND_ROOT/logs/<id>/,
# and log folders older than 7 days are deleted at the start of each run.
#
# Settings (environment, all optional):
#   PRELAND_ROOT    folder for logs, the lock and temp files (default: $HOME/prelanding)
#   PRELAND_REQUESTERS  subscriber ids allowed to request a run with --message (default: Operator)
#   PRELAND_CHROME  Chrome for Testing binary for the CDP suites (default: the runner's own lookup)
#   PRELAND_SHARDS  CDP shards (default 3; use 1 on the operator's Mac)
#   PRELAND_TIMEOUT whole-run limit in minutes (default 75)
#   GOCACHE, GOMODCACHE, npm_config_cache   caches, as usual
#
# Exit status: 0 when every step passed, 1 when any failed or the run timed out, 2 when the request
# was refused or another check is running.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
umask 022

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
refuse() { echo "REFUSED: $*"; exit 2; }

# ── the request ──────────────────────────────────────────────────────────────────────────────────
if [ "${1:-}" = "--message" ]; then
  msg=${2:-}
  case "$msg" in ''|*[!0-9]*) refuse "--message takes a board message id (digits)";; esac
  [ ${#msg} -le 12 ] || refuse "message id too long"
  # Read it while this shell still has the agent's board settings (cleared below), as JSON: the
  # sender check needs subscriber_id, which the text form doesn't show (it shows the job title).
  json=$(coral-board read --id "$msg" --json 2>/dev/null) || refuse "can't read message $msg"
  # One message: its sender and its text (python3 -I: no site or current-folder imports).
  sender=$(printf '%s' "$json" | python3 -I -c 'import json,sys; print(json.loads(sys.stdin.readline()).get("subscriber_id",""))' 2>/dev/null) \
    || refuse "message $msg is not readable"
  # Only the Operator (or a configured requester) may ask for a run, so another agent or person on
  # the board can't trigger one.
  case " ${PRELAND_REQUESTERS:-Operator} " in
    *" $sender "*) [ -n "$sender" ] || refuse "message $msg has no sender";;
    *) refuse "message $msg is not from an allowed requester";;
  esac
  text=$(printf '%s' "$json" | python3 -I -c 'import json,sys; print(json.loads(sys.stdin.readline()).get("content",""))' 2>/dev/null)
  line=$(printf '%s\n' "$text" | grep -oE 'PRELAND-CHECK ref=[^ ]+ id=[^ ]+' | head -1)
  [ -n "$line" ] || refuse "message $msg has no 'PRELAND-CHECK ref=<ref> id=<id>' line"
  ref=${line#PRELAND-CHECK ref=}; ref=${ref%% id=*}
  id=${line##* id=}
else
  ref=${1:-}
  # A default id only when none is given; an empty one is refused below.
  if [ $# -ge 2 ]; then id=$2; else id=$(date -u +%Y%m%dt%H%M%S); fi
fi

# A ref: 40 hex, or a branch or tag name of letters, digits, '.', '_', '/', '-' (1 to 100), without
# "..", not starting with '-', and never a pull request ref. An id: [a-z0-9-], 1 to 32.
if ! [[ $ref =~ ^[0-9a-f]{40}$ ]]; then
  [[ $ref =~ ^[A-Za-z0-9._/-]{1,100}$ ]] || refuse "malformed ref"
  case "$ref" in -*|*..*|refs/pull/*|pull/*) refuse "ref not allowed";; esac
fi
[[ $id =~ ^[a-z0-9-]{1,32}$ ]] || refuse "malformed id (use a-z, 0-9 and '-', up to 32)"

# ── environment ──────────────────────────────────────────────────────────────────────────────────
# No agent identity or production links (Cloud Hub URL, hub mode, tmux socket, data dir): a check
# never talks to a live Coral.
# shellcheck source=../lib/agent_env.sh
. "$repo/tests/lib/agent_env.sh"
unset CORAL_URL CORAL_DIR CORAL_DATA_DIR CORAL_HOST CORAL_PORT TMUX

root=${PRELAND_ROOT:-$HOME/prelanding}
mkdir -p "$root/logs" "$root/tmp"
find "$root/logs" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf {} + 2>/dev/null

# One check at a time (flock on Linux; a lock folder where flock doesn't exist, e.g. macOS).
wt=""
unlock() { :; }
cleanup() {
  [ -n "$wt" ] && { git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1; rm -rf "$wt"; }
  unlock
}
trap cleanup EXIT
trap 'exit 130' INT TERM
if command -v flock >/dev/null 2>&1; then
  exec 9>"$root/lock"
  flock -n 9 || refuse "another check is running"
else
  mkdir "$root/lock.d" 2>/dev/null || refuse "another check is running (or a stale lock folder)"
  unlock() { rmdir "$root/lock.d" 2>/dev/null; }
fi

logs="$root/logs/$id"
[ -e "$logs" ] && refuse "id '$id' was already used"
mkdir -p "$logs"

# ── the commit ───────────────────────────────────────────────────────────────────────────────────
git -C "$repo" fetch -q --prune --tags origin > "$logs/fetch.log" 2>&1 || refuse "git fetch failed"
if [[ $ref =~ ^[0-9a-f]{40}$ ]]; then
  sha=$(git -C "$repo" rev-parse -q --verify "$ref^{commit}") || refuse "unknown commit"
else
  sha=$(git -C "$repo" rev-parse -q --verify "refs/remotes/origin/$ref^{commit}" \
    || git -C "$repo" rev-parse -q --verify "refs/tags/$ref^{commit}") || refuse "no branch or tag '$ref' on origin"
fi
# Only code already on a branch or tag of origin (a fork's commit can be fetched through the parent
# repo, but no branch of ours contains it).
if [ -z "$(git -C "$repo" branch -r --contains "$sha" 2>/dev/null)" ] && \
   [ -z "$(git -C "$repo" tag --contains "$sha" 2>/dev/null)" ]; then
  refuse "$sha is not on any branch or tag of origin"
fi

wt="$root/tmp/wt-$id"
git -C "$repo" worktree add -q --detach "$wt" "$sha" > "$logs/worktree.log" 2>&1 || refuse "worktree failed"
export TMPDIR="$root/tmp" CDP_FULL=1

# ── the check ────────────────────────────────────────────────────────────────────────────────────
deadline=$(( $(date +%s) + ${PRELAND_TIMEOUT:-75} * 60 ))
have_timeout=$(command -v timeout || true)
failed=0
echo "check $sha (log id $id)"

step() { # step <name> <log> <dir> <command...>: run quietly within the deadline, print one line
  local name=$1 log=$2 dir=$3 left; shift 3
  left=$(( deadline - $(date +%s) ))
  if [ $left -le 0 ]; then echo "$name SKIPPED (time limit)"; failed=1; return 1; fi
  if [ -n "$have_timeout" ]; then set -- timeout -k 30 "$left" "$@"; fi
  if (cd "$dir" && nice -n 10 "$@") > "$logs/$log" 2>&1; then echo "$name ok"; return 0; fi
  echo "$name FAILED"; failed=1; return 1
}
failures() { grep -E '^(FAIL|--- FAIL)' "$logs/$1" | sed -E 's/[[:space:]]+[0-9.]+s$//'; }

out=$(cd "$wt/coral-go" && gofmt -l .)
if [ -n "$out" ]; then echo "gofmt needed:"; echo "$out"; failed=1; else echo "gofmt ok"; fi
step vet vet.log "$wt/coral-go" go vet ./...
step "go test" gotest.log "$wt/coral-go" go test -count=1 ./... || failures gotest.log
step "go test (dev fts5)" gotest-dev.log "$wt/coral-go" go test -count=1 -tags 'dev fts5' ./... || failures gotest-dev.log
step "frontend dependencies" npm.log "$wt/tests/frontend" npm install --no-audit --no-fund --no-package-lock
step test:cloud cloud.log "$wt/tests/frontend" npm run -s test:cloud
set -- node run_cdp_suites.mjs --shards "${PRELAND_SHARDS:-3}" --timeout 180
[ -n "${PRELAND_CHROME:-}" ] && set -- "$@" --chrome "$PRELAND_CHROME"
step CDP cdp.log "$wt/tests/frontend" "$@"
grep -E '^(FAIL|FLAKY|TIMEOUT|network:|note:)|suites passed' "$logs/cdp.log" | sed -E 's/ *(results|coral logs):.*$//'
# A failed CDP run keeps results.json and coral.log under TMPDIR: move them next to this run's logs.
for d in "$TMPDIR"/coral-cdp-*; do [ -d "$d" ] && mv "$d" "$logs/"; done

if [ $failed -eq 0 ]; then echo "RESULT: PASS $sha (log id $id)"; else echo "RESULT: FAIL $sha (log id $id)"; fi
exit $failed
