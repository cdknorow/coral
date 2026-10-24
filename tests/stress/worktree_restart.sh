#!/usr/bin/env bash
#
# The restart proof (#6915): two 5-minute cron jobs with a run alive, the server stopped and started again on the same data folder. It records how each run is settled, the next slot, and the worktrees:
# whether the team is offered for resurrect and comes back with its branch rows, what Settings' "Kept worktrees" lists, how often the cron job fires around the restart (once per slot), and whether
# anything is left orphaned (a folder with no registration, a registration with no folder, a branch with neither). A findings run: it prints a table and exits 0 whenever the run itself worked.
#
#   [WORKTREE_MATRIX_OUT=<folder for the table>]  tests/stress/worktree_restart.sh
#
# Safe by construction (as worktree_matrix.sh): its own HOME, data folder, repositories, stub agent CLIs and mock agents under one temp folder; a port other than 8420; it removes only what it made and
# stops only the processes it started.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
NEW_SRC="$(cd "$SCRIPT_DIR/../.." && pwd)"
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
mkdir -p "$TMPROOT/host-home" "$TMPROOT/repos" "$TMPROOT/new"
NEW_VER=$(git -C "$NEW_SRC" log -1 --format='%h %s' 2>/dev/null | cut -c1-60)
log "build: $NEW_VER | git: $(git --version) | os: $(uname -s) | backend: $CORAL_TEST_BACKEND"
build() { (cd "$1/coral-go" && go build -tags "dev fts5" -o "$2/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync 2>&1 | tail -3); }
log "building both ..."
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


# ── a clean repository with two jobs (the same slot): one run is left clean, one gets an unsaved file ─────
start_server "$TMPROOT/new" first || exit 2
coral_test_require_ok "$(api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}")" "cli_path_claude" >/dev/null
JIDS=()
for n in clean dirty; do
    printf '{"name": "restart-%s", "cron_expr": "*/5 * * * *", "timezone": "UTC", "agent_type": "claude", "repo_path": "%s", "prompt": "x", "enabled": 1, "max_duration_s": 600, "cleanup_worktree": 1}' "$n" "$(native "$R")" >"$TMPROOT/job-$n.json"
    resp=$(api_code POST "/api/scheduled/jobs" --data-binary "@$(native "$TMPROOT/job-$n.json")"); code="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    JIDS+=("$(printf '%s' "$body" | jget id)")
    row job-created "job $n (cron */5, max duration 600 s, cleanup on)" "http=$code id=${JIDS[${#JIDS[@]}-1]:+set}"
done
JC=${JIDS[0]}; JD=${JIDS[1]}
secs_to_slot() { python3 -c "import time;n=time.time();print(int(300-(n%300)))"; }
runs_of() { api GET "/api/scheduled/jobs/$1/runs"; }
fmt_runs() { python3 -c "import sys,json,collections
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    print(len(r),'run(s):',[(x.get('scheduled_at','')[11:16],x.get('status'),x.get('exit_reason'),'worktree' if x.get('worktree_path') else 'no-worktree') for x in sorted(r,key=lambda x:x.get('scheduled_at',''))])
except Exception as e: print('?',e)" | tr -d '\r'; }

w=$(secs_to_slot); [[ $w -lt 60 ]] && w=$((w + 300)); log "waiting $w s for the next slot; then 45 s for both runs to be alive"; sleep "$w"; sleep 45
rc=$(runs_of "$JC"); rd=$(runs_of "$JD")
row runs-alive "both runs after the slot, before the restart" "clean job: $(printf '%s' "$rc" | fmt_runs) | dirty job: $(printf '%s' "$rd" | fmt_runs)"
DWT=$(printf '%s' "$rd" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    print(r[0].get('worktree_path') or '')
except Exception: print('')" | tr -d '\r')
[[ -n "$DWT" && -d "$(upath "$DWT")" ]] && echo "unsaved work" >"$(upath "$DWT")/unsaved.txt"
row dirty-file "an unsaved file written into the dirty job's run worktree" "$([[ -n "$DWT" && -f "$(upath "$DWT")/unsaved.txt" ]] && echo written || echo NOT-written)"
row disk-before-restart "worktrees while both runs are alive" "$(disk_state)"

# ── restart: the server stops while the runs are alive (the pty backend ends the agents with it) ────────
STOP_AT=$(date -u +%H:%M:%S)
stop_server
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then taskkill //IM mock-agent.exe //F >/dev/null 2>&1 || true; fi   # a throwaway runner: the agents die with the server on Windows
sleep 3
start_server "$TMPROOT/new" second || exit 2
START_AT=$(date -u +%H:%M:%S)
sleep 15
rc=$(runs_of "$JC"); rd=$(runs_of "$JD")
row runs-settled "both runs after the restart (stopped $STOP_AT, started $START_AT UTC)" "clean job: $(printf '%s' "$rc" | fmt_runs) | dirty job: $(printf '%s' "$rd" | fmt_runs)"
row disk-after-restart "worktrees after the restart (clean one removed, dirty one kept; unsaved file: $([[ -n "$DWT" && -f "$(upath "$DWT")/unsaved.txt" ]] && echo yes || echo NO))" "$(disk_state)"
row kept-worktrees "GET /api/worktrees after the restart" "$(api GET "/api/worktrees" | sed -e 's#b-[a-z0-9]\{10\}#b-ID#g' | clean x 500)"

# ── the next slot: one run per job ────────────────────────────────────────────────────────────────────
w=$(secs_to_slot); log "waiting $((w + 60)) s for the next slot"; sleep "$((w + 60))"
rc=$(runs_of "$JC"); rd=$(runs_of "$JD")
row next-slot "every run of each job after the next slot" "clean job: $(printf '%s' "$rc" | fmt_runs) | dirty job: $(printf '%s' "$rd" | fmt_runs)"
per_slot() { python3 -c "import sys,json,collections
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    s=collections.Counter(x.get('scheduled_at','')[:16] for x in r); print('one run per slot:',all(v==1 for v in s.values()),dict(sorted(s.items())))
except Exception as e: print('?',e)" | tr -d '\r'; }
row one-per-slot "slots with more than one run" "clean job: $(printf '%s' "$rc" | per_slot) | dirty job: $(printf '%s' "$rd" | per_slot)"
api PUT "/api/scheduled/jobs/${JC}" -d '{"enabled": 0}' >/dev/null; api PUT "/api/scheduled/jobs/${JD}" -d '{"enabled": 0}' >/dev/null
sleep 5
row disk-end "worktrees at the end (the run left alive by the last slot is still running)" "$(disk_state)"

TABLE="$TMPROOT/worktree_restart.md"
{
    echo "# scheduler restart proof: $(uname -s), $(git --version), backend $CORAL_TEST_BACKEND"; echo
    echo "build: $NEW_VER"; echo
    echo "| step | what | result |"; echo "|---|---|---|"
    while IFS=$'\t' read -r a b c; do printf '| %s | %s | %s |\n' "$a" "$b" "$c"; done <"$ROWS"
} >"$TABLE"
echo; cat "$TABLE"
if [[ -n "$OUT_DIR" ]]; then mkdir -p "$OUT_DIR"; cp "$TABLE" "$OUT_DIR/worktree_restart.md"; for l in first second; do sed -e "s#$TMPROOT#<tmp>#g" "$TMPROOT/server-$l.log" | grep -aE "scheduler|job_scheduler|stale|adopt|reap|worktree|ended while" | cut -c1-300 | tail -60 >"$OUT_DIR/server_${l}_lines.log" 2>/dev/null || true; done; fi
coral_test_sandbox_check || log "WARNING: the sandbox check failed (a real CLI may have been reached)"
exit 0
