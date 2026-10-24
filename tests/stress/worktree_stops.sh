#!/usr/bin/env bash
#
# The stop proof (#6921): every way Windows ends Coral (Ctrl-C, Ctrl-Break, the console window closed, the tray asking its server to stop, a forced kill), with a scheduled run alive in a clean worktree.
# For each it records, before and after the server has ended, whether the run is settled, its worktree is removed and its agent is alive. A findings run: it prints a table and exits 0 whenever the run itself worked.
#
#   [WORKTREE_MATRIX_OUT=<folder for the table>]  tests/stress/worktree_stops.sh
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
build() { (cd "$1/coral-go" && go build -tags "dev fts5" -o "$2/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync ./cmd/coral-tray 2>&1 | tail -3); }
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



(cd "$NEW_SRC" && go build -o "$TMPROOT/winstop.exe" tests/stress/winstop/main.go) || exit 2
agents_alive() { tasklist //FI "IMAGENAME eq mock-agent.exe" //NH 2>/dev/null | grep -c "mock-agent.exe" | tr -d '\r\n '; }
run_row() { sqlite_row "$1"; }
sqlite_row() {   # the newest run of the data folder's database, read after the server ended
    python3 - "$(native "$1/sessions.db")" <<'PY' 2>&1 | tr -d '\r'
import sqlite3, sys
try:
    c = sqlite3.connect("file:" + sys.argv[1].replace("\\", "/") + "?mode=ro", uri=True)
    r = c.execute("select status, coalesce(exit_reason,''), (worktree_path is not null and worktree_path != ''), coalesce(finished_at,'') != '' from scheduled_runs order by id desc limit 1").fetchone()
    print("status=%s exit_reason=%s worktree_recorded=%s finished_at_set=%s" % (r[0], r[1] or "-", bool(r[2]), bool(r[3])) if r else "no run row")
except Exception as e:
    print("unreadable:", str(e)[:80])
PY
}

# case NAME MODE PROGRAM (coral | tray): a server of that kind with a job every minute (cleanup on, max 600 s); once its run is alive in a clean worktree the server is stopped by MODE
stop_case() {
    local name="$1" mode="$2" prog="$3"
    local dd="$TMPROOT/d-$name" rp="$TMPROOT/repos/$name"
    mkdir -p "$dd" "$rp"; git -C "$rp" init -q -b main; echo hi >"$rp/README.md"; git -C "$rp" add .; git -c user.name=Test -c user.email=test@test.invalid -C "$rp" commit -q -m initial
    export CORAL_DATA_DIR="$dd"   # curl (tests/lib/agent_env.sh) reads this folder's api_key; the server itself gets the native path below
    local go="$TMPROOT/go-$name" out="$TMPROOT/out-$name" lg="$TMPROOT/log-$name"
    rm -f "$go" "$out"
    local prog_args
    if [[ "$prog" == tray ]]; then prog_args=("$(native "$TMPROOT/new/coral-tray${CORAL_TEST_EXE}")" --server-child --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND")
    else prog_args=("$(native "$TMPROOT/new/coral${CORAL_TEST_EXE}")" --home "$(native "$dd")" --no-browser --cloud-url off --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND"); fi
    CORAL_DATA_DIR="$(native "$dd")" HOME="$TMPROOT/host-home" USERPROFILE="$TMPROOT/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
        "$TMPROOT/winstop.exe" -mode "$mode" -go "$(native "$go")" -out "$(native "$out")" -log "$(native "$lg")" -- "${prog_args[@]}" &
    local wpid=$!
    if ! wait_for_server; then row "$name" "$mode ($prog)" "the server did not start: $(tail -c 300 "$lg" 2>/dev/null | clean x 300)"; kill "$wpid" 2>/dev/null; return; fi
    api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}" >/dev/null
    printf '{"name": "stop-%s", "cron_expr": "* * * * *", "timezone": "UTC", "agent_type": "claude", "repo_path": "%s", "prompt": "x", "enabled": 1, "max_duration_s": 600, "cleanup_worktree": 1}' "$name" "$(native "$rp")" >"$TMPROOT/job-$name.json"
    local jresp jid; jresp=$(api POST "/api/scheduled/jobs" --data-binary "@$(native "$TMPROOT/job-$name.json")"); jid=$(printf '%s' "$jresp" | jget id)
    local n=0 wt="" st=""
    while [[ $n -lt 55 ]]; do
        sleep 2; n=$((n + 1))
        local runs; runs=$(api GET "/api/scheduled/jobs/${jid}/runs")
        wt=$(printf '%s' "$runs" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    x=[y for y in r if y.get('status')=='running' and y.get('worktree_path')]
    print(x[0]['worktree_path'] if x else '')
except Exception: print('')" | tr -d '\r')
        [[ -n "$wt" ]] && break
    done
    if [[ -z "$wt" ]]; then row "$name" "$mode ($prog)" "no run came alive in 110 s; job create answered: $(printf '%s' "$jresp" | clean x 160); runs: $(api GET "/api/scheduled/jobs/${jid}/runs" | clean x 240)"; kill "$wpid" 2>/dev/null; return; fi
    sleep 3
    local before_wt before_ag; before_wt=$([[ -d "$(upath "$wt")" ]] && echo present || echo gone); before_ag=$(agents_alive)
    touch "$go"
    n=0; while [[ ! -s "$out" && $n -lt 80 ]]; do sleep 0.5; n=$((n + 1)); done
    sleep 2
    local after_wt after_ag res; after_wt=$([[ -d "$(upath "$wt")" ]] && echo present || echo REMOVED); after_ag=$(agents_alive)
    res=$(cat "$out" 2>/dev/null | clean x 200)
    local shut; shut=$(sed -e "s#$TMPROOT#<tmp>#g" "$lg" 2>/dev/null | grep -aE "SHUTDOWN|cleaned up worktree|finish|worktree" | cut -c21-140 | tail -4 | tr '\n' ';')
    row "$name" "$mode ($prog): run alive in a clean worktree (before: worktree $before_wt, agents alive $before_ag, run running)" "stop: $res | after: worktree $after_wt, agents alive $after_ag, run row: $(sqlite_row "$dd") | log: ${shut:-none}"
    wait "$wpid" 2>/dev/null
    [[ -n "${GITHUB_ACTIONS:-}" ]] && taskkill //IM mock-agent.exe //F >/dev/null 2>&1
    # the next start on the same data folder settles the run the stop left (#6898): read its row through the API
    env HOME="$TMPROOT/host-home" USERPROFILE="$TMPROOT/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" "$TMPROOT/new/coral${CORAL_TEST_EXE}" --home "$(native "$dd")" --no-browser --cloud-url off --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMPROOT/restart-$name.log" 2>&1 &
    local rpid=$!
    if wait_for_server; then
        sleep 12
        local r1; r1=$(api GET "/api/scheduled/jobs/${jid}/runs" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); r=d if isinstance(d,list) else d.get('runs',[])
    x=sorted(r,key=lambda y:y.get('id',0))[0]
    print('first run: status=%s exit_reason=%s' % (x.get('status'), x.get('exit_reason') or '-'))
except Exception as e: print('?',e)" | tr -d '\r')
        row "$name-restart" "$name: the next start on the same data folder (after the stop above)" "$r1; its worktree: $([[ -d "$(upath "$wt")" ]] && echo present || echo removed)"
    else
        row "$name-restart" "$name: the next start" "the server did not start"
    fi
    kill "$rpid" 2>/dev/null; wait "$rpid" 2>/dev/null
    [[ -n "${GITHUB_ACTIONS:-}" ]] && taskkill //IM mock-agent.exe //F >/dev/null 2>&1
    return 0
}

stop_case ctrlc ctrlc coral
stop_case ctrlbreak ctrlbreak coral
stop_case close close coral
stop_case traystdin stdin tray
stop_case kill kill coral

TABLE="$TMPROOT/worktree_stops.md"
{
    echo "# every way Windows ends Coral, with a run alive: $(uname -s), $(git --version), backend $CORAL_TEST_BACKEND"; echo
    echo "build: $NEW_VER"; echo
    echo "| case | what | result |"; echo "|---|---|---|"
    while IFS=$'\t' read -r a b c; do printf '| %s | %s | %s |\n' "$a" "$b" "$c"; done <"$ROWS"
} >"$TABLE"
echo; cat "$TABLE"
if [[ -n "$OUT_DIR" ]]; then mkdir -p "$OUT_DIR"; cp "$TABLE" "$OUT_DIR/worktree_stops.md"; for f in "$TMPROOT"/log-*; do sed -e "s#$TMPROOT#<tmp>#g" "$f" | cut -c1-300 | tail -80 >"$OUT_DIR/server_$(basename "$f").log" 2>/dev/null || true; done; fi
coral_test_sandbox_check || log "WARNING: the sandbox check failed (a real CLI may have been reached)"
exit 0
