#!/usr/bin/env bash
#
# The upgrade proof (#6882): a 2-member worktree team and a 5-minute cron job running on the OLD build, the server stopped, the NEW build started on the same data folder. It records what comes back:
# whether the team is offered for resurrect and comes back with its branch rows, what Settings' "Kept worktrees" lists, how often the cron job fires around the restart (once per slot), and whether
# anything is left orphaned (a folder with no registration, a registration with no folder, a branch with neither). A findings run: it prints a table and exits 0 whenever the run itself worked.
#
#   WTU_OLD_SRC=<a checkout of the old version>  [WORKTREE_MATRIX_OUT=<folder for the table>]  tests/stress/worktree_upgrade.sh
#
# Safe by construction (as worktree_matrix.sh): its own HOME, data folder, repositories, stub agent CLIs and mock agents under one temp folder; a port other than 8420; it removes only what it made and
# stops only the processes it started.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
NEW_SRC="$(cd "$SCRIPT_DIR/../.." && pwd)"
OLD_SRC="${WTU_OLD_SRC:-}"
[[ -d "$OLD_SRC/coral-go" ]] || { echo "set WTU_OLD_SRC to a checkout of the old version" >&2; exit 2; }
PORT=${CORAL_TEST_PORT:-8478}
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
OUT_DIR="${WORKTREE_MATRIX_OUT:-}"
SERVER_PID=""

log() { echo "[worktree-upgrade] $*"; }
TMPROOT="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-wtu.XXXXXX")"
export TMPDIR="$TMPROOT"
cleanup() {
    local code=$?
    stop_server
    coral_test_sandbox_cleanup 2>/dev/null || true
    if [[ "${KEEP_TEST_DATA:-0}" == 1 || $code -ne 0 ]]; then log "test data kept: $TMPROOT"; else coral_test_rm_tree "$TMPROOT"; fi
}
trap cleanup EXIT

api() { local method="$1" path="$2"; shift 2; curl -s -m 60 -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "$@"; }
api_code() { local method="$1" path="$2"; shift 2; curl -s -m 90 -w '\n%{http_code}' -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "$@"; }
jget() { python3 -c "import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print(''); sys.exit(0)
v=d
for k in sys.argv[1].split('.'):
    v=v.get(k,'') if isinstance(v,dict) else ''
print('' if v is None else (v if not isinstance(v,(dict,list)) else json.dumps(v)))" "$1" 2>/dev/null | tr -d '\r'; }
native() { coral_test_native_path "$1"; }
upath() { cygpath -u "$1" 2>/dev/null || printf '%s' "$1"; }

wait_for_server() { local n=60; while ! curl -s -m 5 "${BASE_URL}/api/health" >/dev/null 2>&1; do n=$((n - 1)); [[ $n -le 0 ]] && return 1; sleep 0.5; done; }
stop_server() {
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
    SERVER_PID=""
}

# start_server BINDIR LABEL: the server of that build on the shared data folder
start_server() {
    local bindir="$1" label="$2"
    stop_server
    export CORAL_DATA_DIR="$TMPROOT/coral-data"
    mkdir -p "$CORAL_DATA_DIR"
    env -u CORAL_PORT -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR \
        HOME="$TMPROOT/host-home" USERPROFILE="$TMPROOT/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
        "$bindir/coral${CORAL_TEST_EXE}" --home "$(native "$CORAL_DATA_DIR")" --no-browser --cloud-url off --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMPROOT/server-$label.log" 2>&1 &
    SERVER_PID=$!
    wait_for_server || { log "the $label server did not start"; tail -20 "$TMPROOT/server-$label.log" >&2; return 1; }
}

export CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 CORAL_TMUX_NO_FALLBACK=1
mkdir -p "$TMPROOT/host-home" "$TMPROOT/repos" "$TMPROOT/old" "$TMPROOT/new"
OLD_VER=$(git -C "$OLD_SRC" log -1 --format='%h %s' 2>/dev/null | cut -c1-60); NEW_VER=$(git -C "$NEW_SRC" log -1 --format='%h %s' 2>/dev/null | cut -c1-60)
log "old build: $OLD_VER | new build: $NEW_VER | git: $(git --version) | os: $(uname -s) | backend: $CORAL_TEST_BACKEND"
build() { (cd "$1/coral-go" && go build -tags "dev fts5" -o "$2/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync 2>&1 | tail -3); }
log "building both ..."
build "$OLD_SRC" "$TMPROOT/old" || exit 2
build "$NEW_SRC" "$TMPROOT/new" || exit 2
(cd "$NEW_SRC/coral-go" && go build -o "$TMPROOT/mock-agent${CORAL_TEST_EXE}" ./cmd/mock-agent/) || exit 2
MOCK_AGENT_NATIVE="$(native "$TMPROOT/mock-agent${CORAL_TEST_EXE}")"
coral_test_sandbox

ROWS="$TMPROOT/rows.tsv"; : >"$ROWS"
clean() { tr '\r\n\t' '   ' | sed -e "s#$TMPROOT#<tmp>#g" | cut -c1-"${2:-500}"; }
row() { printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$ROWS"; log "[$1] $2 | $3" | cut -c1-900; [[ -n "$OUT_DIR" ]] && { mkdir -p "$OUT_DIR"; cp "$ROWS" "$OUT_DIR/upgrade_rows.tsv" 2>/dev/null; }; }

R="$TMPROOT/repos/upg"; mkdir -p "$R"; git -C "$R" init -q -b main; echo hello >"$R/README.md"; git -C "$R" add .; git -c user.name=Test -c user.email=test@test.invalid -C "$R" commit -q -m initial

# what is on disk and in git for the worktrees of this repository: folders, registrations, branches, and what is orphaned
disk_state() {
    local dirs regs brs orphan_f orphan_r
    dirs=$(ls "$(upath "$CORAL_DATA_DIR")/worktrees" 2>/dev/null | sort | tr '\n' ' ')
    regs=$(git -C "$R" worktree list --porcelain 2>/dev/null | sed -n 's/^worktree //p' | sed -e 's#\\#/#g' | grep -- "/worktrees/" | sed 's#.*/worktrees/##' | sort | tr '\n' ' ')
    brs=$(git -C "$R" branch --list 'coral-team/*' 'coral/*' 2>/dev/null | tr -d ' *+\r' | sort | tr '\n' ' ')
    orphan_f=$(comm -23 <(printf '%s\n' $dirs | sort) <(printf '%s\n' $regs | sort) | tr '\n' ' ')
    orphan_r=$(comm -13 <(printf '%s\n' $dirs | sort) <(printf '%s\n' $regs | sort) | tr '\n' ' ')
    printf 'folders=[%s] registered=[%s] branches=[%s] FOLDER-WITHOUT-REGISTRATION=[%s] REGISTRATION-WITHOUT-FOLDER=[%s]' "$dirs" "$regs" "$brs" "$orphan_f" "$orphan_r" | sed -e 's#b-[a-z0-9]\{10\}#b-ID#g' -e 's#job-run-[0-9]*[-a-z]*#job-run-N#g'
}

# ── the old build: a 2-member worktree team, and a job every five minutes ─────────────────────────────
start_server "$TMPROOT/old" old || exit 2
coral_test_require_ok "$(api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}")" "cli_path_claude" >/dev/null
printf '{"board_name": "upg-team", "working_dir": "%s", "worktree": true, "agents": [{"agent_type": "claude", "name": "Dev", "prompt": "work"}, {"agent_type": "claude", "name": "QA", "prompt": "test"}]}' "$(native "$R")" >"$TMPROOT/body.json"
resp=$(api_code POST "/api/sessions/launch-team" --data-binary "@$(native "$TMPROOT/body.json")"); code="${resp##*$'\n'}"; body="${resp%$'\n'*}"
BID=$(printf '%s' "$body" | jget board_id)
row old-team-launch "http=$code board=<id> agents=$(printf '%s' "$body" | python3 -c "import sys,json
try:
    print(len(json.load(sys.stdin).get('agents',[])))
except Exception: print('?')" | tr -d '\r')" "$(disk_state)"
printf '{"name": "upgrade-cron", "cron_expr": "*/5 * * * *", "timezone": "UTC", "agent_type": "claude", "repo_path": "%s", "prompt": "x", "enabled": 1, "max_duration_s": 40, "cleanup_worktree": 1}' "$(native "$R")" >"$TMPROOT/job.json"
resp=$(api_code POST "/api/scheduled/jobs" --data-binary "@$(native "$TMPROOT/job.json")"); code="${resp##*$'\n'}"; body="${resp%$'\n'*}"
JID=$(printf '%s' "$body" | jget id); [[ -z "$JID" ]] && JID=$(printf '%s' "$body" | jget job_id)
row old-job-created "http=$code job=${JID:+id}" "$(printf '%s' "$body" | clean x 200)"

secs_to_slot() { python3 -c "import time;n=time.time();print(int(300-(n%300)))"; }
# wait for the first slot to fire on the OLD build, then 90 s more for its run to end
w=$(secs_to_slot); [[ $w -lt 100 ]] && w=$((w + 300)); log "waiting $w s for the next five-minute slot (the old build runs it)"; sleep "$w"; sleep 90
runs_old=$(api GET "/api/scheduled/jobs/${JID}/runs")
row old-runs-before-stop "runs seen on the old build" "$(printf '%s' "$runs_old" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    print(len(r),'run(s):',[(x.get('scheduled_at','')[11:16],x.get('status'),x.get('exit_reason')) for x in r])
except Exception as e: print('?',e)" | tr -d '\r')"
team_before=$(api GET "/api/teams/detail/${BID}")
row old-team-before-stop "team status" "status=$(printf '%s' "$team_before" | jget status)"

# ── stop the old build, start the new on the same data folder ─────────────────────────────────────────
STOP_AT=$(date -u +%H:%M:%S)
stop_server
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then taskkill //IM mock-agent.exe //F >/dev/null 2>&1 || true; fi   # a throwaway runner: the old server's mock agents die with it on Windows
row stop-old "the old server stopped at $STOP_AT UTC" "$(disk_state)"
start_server "$TMPROOT/new" new || exit 2
START_AT=$(date -u +%H:%M:%S)
sleep 5
team_after=$(api GET "/api/teams/detail/${BID}")
live=$(api GET "/api/sessions/live")
row new-started "new server up at $START_AT UTC (stopped $STOP_AT)" "team status=$(printf '%s' "$team_after" | jget status); live sessions listed: $(printf '%s' "$live" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('sessions',d.get('rows',[]))
    print(len(r))
except Exception: print('?')" | tr -d '\r')"
kept=$(api GET "/api/worktrees")
row new-kept-worktrees "GET /api/worktrees before any resurrect" "$(printf '%s' "$kept" | sed -e 's#b-[a-z0-9]\{10\}#b-ID#g' | clean x 500)"
row new-disk-after-start "disk state on the new build" "$(disk_state)"
if [[ "$(printf '%s' "$team_after" | jget status)" == stopped ]]; then
    resp=$(api_code POST "/api/teams/detail/${BID}/resurrect" -d '{}'); code="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    sleep 4
    live2=$(api GET "/api/sessions/live")
    row new-resurrect "POST resurrect (the team was stopped: offered)" "http=$code agents=$(printf '%s' "$body" | python3 -c "import sys,json
try:
    print(len(json.load(sys.stdin).get('agents',[])))
except Exception: print('?')" | tr -d '\r'); live rows carrying a branch: $(printf '%s' "$live2" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('sessions',d.get('rows',[]))
    print(sum(1 for x in r if isinstance(x,dict) and any('branch' in k and x[k] for k in x)))
except Exception: print('?')" | tr -d '\r')"
else
    row new-resurrect "the team is not 'stopped' after the restart, so nothing to resurrect" "status=$(printf '%s' "$team_after" | jget status)"
    live2=$live
fi
row new-disk-after-resurrect "disk state after the resurrect step" "$(disk_state)"

# ── the next slot fires on the new build ──────────────────────────────────────────────────────────────
w=$(secs_to_slot); log "waiting $((w + 90)) s for the next slot on the new build and its run"; sleep "$((w + 90))"
runs_all=$(api GET "/api/scheduled/jobs/${JID}/runs")
row runs-per-slot "every run of the job, by slot (UTC minute), status, reason" "$(printf '%s' "$runs_all" | python3 -c "import sys,json,collections
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    slots=collections.Counter(x.get('scheduled_at','')[:16] for x in r)
    print(len(r),'run(s); per slot:',dict(sorted(slots.items())),'| one run per slot:',all(v==1 for v in slots.values()),'|',[(x.get('scheduled_at','')[11:16],x.get('status'),x.get('exit_reason')) for x in sorted(r,key=lambda x:x.get('scheduled_at',''))])
except Exception as e: print('?',e)" | tr -d '\r')"

# ── stop the team and the job; what is left ───────────────────────────────────────────────────────────
api PUT "/api/scheduled/jobs/${JID}" -d '{"enabled": 0}' >/dev/null
resp=$(api_code POST "/api/sessions/live/team/${BID}/kill" -d '{}'); code="${resp##*$'\n'}"
sleep 8
row final-stop-team "POST Stop team on the new build" "http=$code worktree answer: $(printf '%s' "${resp%$'\n'*}" | jget worktree | sed -e 's#b-[a-z0-9]\{10\}#b-ID#g' | clean x 200)"
row final-disk "disk state at the end" "$(disk_state)"
kept2=$(api GET "/api/worktrees"); row final-kept-worktrees "GET /api/worktrees at the end" "$(printf '%s' "$kept2" | sed -e 's#b-[a-z0-9]\{10\}#b-ID#g' | clean x 300)"

TABLE="$TMPROOT/worktree_upgrade.md"
{
    echo "# worktree upgrade proof: $(uname -s), $(git --version), backend $CORAL_TEST_BACKEND"; echo
    echo "old: $OLD_VER"; echo "new: $NEW_VER"; echo
    echo "| step | what | result |"; echo "|---|---|---|"
    while IFS=$'\t' read -r a b c; do printf '| %s | %s | %s |\n' "$a" "$b" "$c"; done <"$ROWS"
} >"$TABLE"
echo; cat "$TABLE"
if [[ -n "$OUT_DIR" ]]; then mkdir -p "$OUT_DIR"; cp "$TABLE" "$OUT_DIR/worktree_upgrade.md"; for l in old new; do sed -e "s#$TMPROOT#<tmp>#g" "$TMPROOT/server-$l.log" | grep -aE "worktree|launch-team|resurrect|kill-team|wake|scheduler|job_scheduler" | tail -60 >"$OUT_DIR/server_${l}_lines.log" 2>/dev/null || true; done; fi
coral_test_sandbox_check || log "WARNING: the sandbox check failed (a real CLI may have been reached)"
exit 0
