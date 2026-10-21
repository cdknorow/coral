#!/usr/bin/env bash
#
# Stress / integration test for solo agent tasks and team task workflows.
#
# A dummy agent (mock-agent standing in for the Claude CLI) is given tasks
# from the "dashboard" API. The test checks what is typed into the agent by
# reading its terminal, then drives the agent side with the real coral-agent
# CLI: claim, current, list, complete, plus concurrent claims and isolation
# between agents.
# Assignment isolation uses real CLI/API requests and a headless browser edit
# regression. Requires tmux, Python 3, Node, Chrome/Chromium (or CORAL_CHROME_BIN),
# and tests/frontend dependencies installed beforehand (npm ci there).
# Readiness, restart recovery, the artifact limits and concurrent completion review are
# covered by the Go tests in coral-go/internal/board.
#
set -euo pipefail
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=${CORAL_TEST_PORT:-8474}
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
DASHBOARD_HEADERS=(-H "Origin: ${BASE_URL}" -H "Sec-Fetch-Site: same-origin")
PASS=0
FAIL=0
SERVER_PID=""

# Keep this integration suite bounded on developer machines and never emit telemetry.
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOMEMLIMIT="${GOMEMLIMIT:-512MiB}"
export CORAL_TELEMETRY_DISABLED=1
export CORAL_NO_DEMO_SEED=1  # no demo workflow/job (it would run a real agent)
export CORAL_NO_WELCOME=1    # no first-launch screen (#3221)

# ── Helpers ──────────────────────────────────────────────────────────

cleanup() {
    local exit_code=$?
    coral_test_watchdog_stop
    for entry in "${NAME_A:-}:${SID_A:-}" "${NAME_B:-other-agent}:${SID_B:-}"; do
        if [[ -n "${entry##*:}" ]]; then
            api POST "/api/sessions/live/${entry%:*}/kill" -d "{\"agent_type\":\"claude\",\"session_id\":\"${entry##*:}\"}" >/dev/null 2>&1 || true
        fi
    done
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    coral_test_sandbox_cleanup   # #4354: its check first (a crash path reads the stub marker), then the folders
    if [[ $exit_code -ne 0 || $FAIL -gt 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log: ${TMPDIR_AT:-}"
    else
        coral_test_rm_tree "${TMPDIR_AT:-}"
    fi
}
trap cleanup EXIT
coral_test_watchdog   # a hang fails this runner within CORAL_TEST_WATCHDOG_S (default 900 s), not the whole run (#116)

log()  { echo "[agent-tasks] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }
check() { if eval "$2"; then pass "$1"; else fail "$1${3:+ — $3}"; fi; }

# The dashboard's requests: same-origin browser headers. Since #3495 (2ff934f4) a route an agent's
# tools call accepts a token-less request only from the Operator (the dashboard), so the suite's
# "dashboard" calls say they are (DASHBOARD_HEADERS, set with BASE_URL).
api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "${DASHBOARD_HEADERS[@]}" "$@"
}

# Read a field from JSON on stdin: jget <python-expression over d>
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }

wait_for_server() {
    local retries=40
    while ! curl -s -m 5 "${BASE_URL}/api/health" >/dev/null 2>&1; do
        retries=$((retries - 1))
        if [[ $retries -le 0 ]]; then
            log "ERROR: Server failed to start on port $PORT"; exit 1
        fi
        sleep 0.5
    done
    log "Server is ready on port $PORT"
}

# Run coral-agent as an agent: its identity is CORAL_SESSION_NAME (TMUX unset
# so the caller's own tmux session, if any, is not picked up).
# token_of <session name>: the session token Coral gave that agent at launch, as the agent itself
# has it (the mock agent writes what it inherited to MOCK_AGENT_ENV_DUMP, in this test's folder).
# Since #3495 an agent's tools carry it on every call. Empty for a name that isn't an agent.
token_of() {
    local sid="${1#claude-}" f attempt
    [[ "$1" == claude-* ]] || return 0 # not an agent's session (the suite's own roles)
    f="$TMPDIR_AT/env-dump/$sid.env"
    for attempt in $(seq 1 40); do
        [[ -s "$f" ]] && break
        sleep 0.25
    done
    [[ -s "$f" ]] && sed -n 's/^CORAL_SESSION_TOKEN=//p' "$f"
}

as_agent() {
    local session_name="$1"; shift
    env -u TMUX -u CORAL_PORT CORAL_URL="$BASE_URL" CORAL_SESSION_NAME="$session_name" \
        CORAL_SESSION_TOKEN="$(token_of "$session_name")" "$TMPDIR_AT/coral-agent" "$@"
}

# The agent's terminal as text
capture() {
    api GET "/api/sessions/live/$1/capture?agent_type=claude&session_id=$2" | jget "d.get('capture') or ''"
}

# Wait until the agent's terminal shows a line, up to ~10s. The pane wraps
# long lines, so they are joined back before searching.
# Up to 150 s: a notice is typed by the automated-input gate, which holds it while the agent is
# in a turn. A mock agent posts no hook events, so its turn ends only when the gate takes it as
# stale (staleInTurn, 2 minutes); a check that passes returns as soon as the text is there.
wait_for_input() {
    local name="$1" sid="$2" needle="$3" i
    for i in $(seq 1 600); do
        if capture "$name" "$sid" | tr -d '\n' | grep -F -q -- "$needle"; then return 0; fi
        sleep 0.25
    done
    return 1
}

launch_agent() {
    local dir="$1" display="$2"
    mkdir -p "$dir"
    api POST "/api/sessions/launch" -d "{\"working_dir\": \"$(coral_test_native_path "$dir")\", \"agent_type\": \"claude\", \"display_name\": \"$display\"}"
}

create_task() {  # name sid title body notify
    python3 -c "import json,sys; print(json.dumps({'title': sys.argv[1], 'body': sys.argv[2], 'session_id': sys.argv[3], 'notify': sys.argv[4] == 'true'}))" "$3" "$4" "$2" "$5" \
        | curl -s -m 15 -X POST "${BASE_URL}/api/sessions/live/$1/tasks" -H "Content-Type: application/json" "${DASHBOARD_HEADERS[@]}" -d @-
}

task_states() {  # name sid -> "id:completed id:completed ..."
    api GET "/api/sessions/live/$1/tasks?session_id=$2" | jget "' '.join(f\"{t['id']}:{t['completed']}\" for t in d)"
}

# ── Setup ────────────────────────────────────────────────────────────

TMPDIR_AT="$(mktemp -d)"
export TMPDIR="$TMPDIR_AT"  # server os.TempDir() droppings land here, removed on exit (#3926)
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    log "ERROR: Port $PORT is already in use; set CORAL_TEST_PORT to a free port."
    exit 1
fi
export CORAL_DATA_DIR="$TMPDIR_AT/coral-data"
mkdir -p "$CORAL_DATA_DIR"

log "Building coral (dev mode), mock-agent and coral-agent..."
cd "$CORAL_DIR"
# This build's coral-board and coral-hook-* next to coral: a dev or test server never uses the
# installed app's (#3492), so without them its agents would send no status events.
go build -p 1 -tags "dev fts5" -o "$TMPDIR_AT/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync
go build -p 1 -o "$TMPDIR_AT/mock-agent${CORAL_TEST_EXE}" ./cmd/mock-agent/
go build -p 1 -o "$TMPDIR_AT/coral-board${CORAL_TEST_EXE}" ./cmd/coral-board/
# The mock as the server reads it: a native path, with .exe on Windows (a Windows server finds a CLI only by its extension).
MOCK_AGENT_NATIVE="$(coral_test_native_path "$TMPDIR_AT/mock-agent${CORAL_TEST_EXE}")"

log "Starting coral server on port $PORT..."
# The server gets its own HOME and no host-data dirs, so it never indexes this machine's real
# ~/.claude/projects, ~/.codex, ~/.gemini or ~/.pi (privacy and deterministic results).
mkdir -p "$TMPDIR_AT/host-home"
mkdir -p "$TMPDIR_AT/env-dump"
coral_test_sandbox   # #4354: stub claude/codex/gemini first on PATH; checked at the end
env -u CORAL_PORT -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR -u CORAL_CLOUD_URL HOME="$TMPDIR_AT/host-home" USERPROFILE="$TMPDIR_AT/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
    MOCK_AGENT_ENV_DUMP="$TMPDIR_AT/env-dump" \
    "$TMPDIR_AT/coral" --home "$CORAL_DATA_DIR" --no-browser --cloud-url off --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMPDIR_AT/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# Strict local auth is the default (#3936): the per-session task routes take an agent's token or
# the dashboard Operator, not the API key, so this harness signs in as the dashboard does: a
# single-use 'coral open' link traded for the session cookie, sent on every dashboard call.
JAR="$TMPDIR_AT/cookies.txt"
LINK=$("$TMPDIR_AT/coral" open --print --url "$BASE_URL" --home "$CORAL_DATA_DIR" | sed -n 's/^ *\(http[^ ]*login_token=[^ ]*\).*/\1/p' | head -1)
[[ -n "$LINK" ]] || { log "ERROR: coral open printed no sign-in link"; exit 1; }
command curl -s -m 10 -o /dev/null -c "$JAR" "$LINK"
COOKIE=$(awk '$6 ~ /^coral_session/ {print $6 "=" $7}' "$JAR" | tail -1)
[[ -n "$COOKIE" ]] || { log "ERROR: the sign-in link gave no session cookie"; exit 1; }
DASHBOARD_HEADERS+=(-H "Cookie: $COOKIE")

# The dummy agent stands in for the Claude CLI
coral_test_require_ok "$(api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}")" "cli_path_claude=$MOCK_AGENT_NATIVE"

# ── Test 1: launch a dummy agent (no board) ──────────────────────────

log "Test 1: Launch a dummy agent..."
L1=$(launch_agent "$TMPDIR_AT/work-a/solo-agent" "Solo Agent")
SID_A=$(echo "$L1" | jget "d.get('session_id','')")
NAME_A=$(echo "$L1" | jget "d.get('name') or d.get('agent_name') or 'solo-agent'")
SESS_A=$(echo "$L1" | jget "d.get('session_name') or ('claude-' + d.get('session_id',''))")
check "dummy agent launched" '[[ -n "$SID_A" ]]' "$L1"
wait_for_input "$NAME_A" "$SID_A" "[mock-agent] started" || true
check "dummy agent is running in its terminal" 'capture "$NAME_A" "$SID_A" | grep -q "mock-agent"'

# ── Test 2: tasks with notify type the claim prompt into the agent ───

log "Test 2: Create tasks and check what is typed into the agent..."
T1=$(create_task "$NAME_A" "$SID_A" "Add a battle log" "Log each round with both cards and the winner." true)
T2=$(create_task "$NAME_A" "$SID_A" "Write engine tests" "Cover ties and forfeits." true)
T3=$(create_task "$NAME_A" "$SID_A" $'Fix the\nlogin form' "" true)
ID1=$(echo "$T1" | jget "d['id']"); ID2=$(echo "$T2" | jget "d['id']"); ID3=$(echo "$T3" | jget "d['id']")
N1=$(echo "$T1" | jget "d.get('notified','')")
check "the create response returns the prompt it sent" '[[ "$N1" == "You have a new task in Coral (#$ID1: Add a battle log). Claim it with \`coral-agent task claim\` to see the details, then run \`coral-agent task complete $ID1\` when it'"'"'s done." ]]' "$N1"
check "task 1's claim prompt reached the agent's terminal" 'wait_for_input "$NAME_A" "$SID_A" "[mock-agent] input: You have a new task in Coral (#$ID1: Add a battle log). Claim it with \`coral-agent task claim\`"'
check "task 2's claim prompt reached the agent's terminal" 'wait_for_input "$NAME_A" "$SID_A" "[mock-agent] input: You have a new task in Coral (#$ID2: Write engine tests)"'
check "a multi-line title is stored and sent as one line" 'wait_for_input "$NAME_A" "$SID_A" "[mock-agent] input: You have a new task in Coral (#$ID3: Fix the login form)"'
check "the task details are not typed into the agent (they come with the claim)" '! capture "$NAME_A" "$SID_A" | grep -q "Cover ties and forfeits"'

# ── Test 3: without notify nothing is typed ──────────────────────────

log "Test 3: A task without notify sends nothing..."
T4=$(create_task "$NAME_A" "$SID_A" "Quiet task" "" false)
ID4=$(echo "$T4" | jget "d['id']")
sleep 1.5
check "no notification field when notify is off" '[[ "$(echo "$T4" | jget "d.get(\"notified\",\"\")")" == "" ]]'
check "nothing about the quiet task reached the terminal" '! capture "$NAME_A" "$SID_A" | grep -q "Quiet task"'

# ── Test 4: the same open title is reused, not duplicated ────────────

log "Test 4: Re-creating an open task's title reuses it..."
T1B=$(create_task "$NAME_A" "$SID_A" "Add a battle log" "" false)
check "an open task with the same title is reused" '[[ "$(echo "$T1B" | jget "d[\"id\"]")" == "$ID1" ]]'
check "the agent has exactly 4 tasks" '[[ $(task_states "$NAME_A" "$SID_A" | wc -w | tr -d " ") -eq 4 ]]'

# ── Test 5: the agent claims, inspects and completes (coral-board output) ─

log "Test 5: add / list / claim / current / complete / cancel via coral-agent..."
OUT=$(as_agent "$SESS_A" task list)
check "task list prints the board-style table with 4 pending tasks" 'echo "$OUT" | grep -qE "^ID +Status +Priority +Assignee +Title" && [[ $(echo "$OUT" | grep -cE "^#[0-9]+ +pending ") -eq 4 ]]' "$OUT"
OUT=$(as_agent "$SESS_A" task current)
check "current with nothing in progress: No active task" '[[ "$OUT" == "No active task" ]]' "$OUT"
OUT=$(as_agent "$SESS_A" task claim)
check "claim: Claimed Task #N (priority): title, then the details" 'echo "$OUT" | grep -qx "Claimed Task #$ID1 (medium): Add a battle log" && echo "$OUT" | grep -q "Log each round with both cards and the winner."' "$OUT"
OUT=$(as_agent "$SESS_A" task current)
check "current: Task #N (priority) [in_progress]: title" 'echo "$OUT" | grep -qx "Task #$ID1 (medium) \[in_progress\]: Add a battle log"' "$OUT"
if OUT=$(as_agent "$SESS_A" task claim 2>&1); then CODE=0; else CODE=$?; fi
check "claim refuses a second active task" '[[ $CODE -ne 0 ]] && echo "$OUT" | grep -q "current task"'
OUT=$(as_agent "$SESS_A" task complete "$ID1" --message "Logged in battle_log.go")
check "complete reports the outcome" '[[ "$OUT" == "Finished Task #$ID1 (success): Add a battle log" ]]' "$OUT"
if OUT=$(as_agent "$SESS_A" task complete "$ID1" 2>&1); then CODE=0; else CODE=$?; fi
check "completed results cannot be overwritten" '[[ $CODE -ne 0 ]]'
OUT=$(as_agent "$SESS_A" task claim)
check "after completion the next claim is task 2" 'echo "$OUT" | grep -qx "Claimed Task #$ID2 (medium): Write engine tests"'
OUT=$(as_agent "$SESS_A" task cancel "$ID4" --message "Not needed")
check "cancel: Cancelled Task #N: title" '[[ "$OUT" == "Cancelled Task #$ID4: Quiet task" ]]' "$OUT"
STATES=$(task_states "$NAME_A" "$SID_A")
check "the dashboard sees done / in progress / pending / cancelled" 'echo " $STATES " | grep -q " $ID1:1 " && echo " $STATES " | grep -q " $ID2:2 " && echo " $STATES " | grep -q " $ID3:0 " && echo " $STATES " | grep -q " $ID4:3 "' "$STATES"
OUT=$(as_agent "$SESS_A" task list)
check "list shows completed, in_progress, pending and skipped" 'echo "$OUT" | grep -qE "^#$ID1 +completed " && echo "$OUT" | grep -qE "^#$ID2 +in_progress " && echo "$OUT" | grep -qE "^#$ID3 +pending " && echo "$OUT" | grep -qE "^#$ID4 +skipped "' "$OUT"
MSG=$(api GET "/api/agent/tasks?session_id=$SID_A" -H "X-Coral-Session-Token: $(token_of "$SESS_A")" | jget "next(t.get('completion_message','') for t in d['tasks'] if t['id']==$ID1)")
check "the completion message is stored" '[[ "$MSG" == "Logged in battle_log.go" ]]' "$MSG"

# ── Test 6: claim order follows priority, like a board ───────────────

log "Test 6: Claim order is by priority, then age..."
as_agent "$SESS_A" task complete "$ID2" >/dev/null
for p in low critical medium high; do as_agent "$SESS_A" task add "Priority $p" --priority "$p" >/dev/null; done
OUT=$(as_agent "$SESS_A" task add "Added by the agent" --body "Self-assigned details" --priority low)
check "task add: Created Task #N: title" 'echo "$OUT" | grep -qE "^Created Task #[0-9]+: Added by the agent$"' "$OUT"
ORDER=""
for i in 1 2 3 4 5; do
    OUT=$(as_agent "$SESS_A" task claim)
    ORDER="$ORDER|$(echo "$OUT" | sed -n 's/^Claimed Task #[0-9]* ([a-z]*): //p')"
    CLAIM_ID=$(echo "$OUT" | sed -n 's/^Claimed Task #\([0-9]*\) .*/\1/p')
    as_agent "$SESS_A" task complete "$CLAIM_ID" >/dev/null
done
# Pending before: task 3 (medium, older), then the new ones
check "claims go critical, high, medium (oldest first), low (oldest first)" '[[ "$ORDER" == "|Priority critical|Priority high|Fix the login form|Priority medium|Priority low" ]]' "$ORDER"
OUT=$(as_agent "$SESS_A" task claim)
check "the agent-added task (low, newest) comes last, with its details" 'echo "$OUT" | grep -qx "Claimed Task #[0-9]* (low): Added by the agent" && echo "$OUT" | grep -q "Self-assigned details"' "$OUT"
CLAIM_ID=$(echo "$OUT" | sed -n 's/^Claimed Task #\([0-9]*\) .*/\1/p')
as_agent "$SESS_A" task complete "$CLAIM_ID" >/dev/null
OUT=$(as_agent "$SESS_A" task claim)
check "with nothing available: No available tasks (exit 0, as coral-board)" '[[ "$OUT" == "No available tasks" ]]' "$OUT"

# ── Test 7: a second agent is isolated ───────────────────────────────

log "Test 7: A second agent cannot see or touch the first agent's tasks..."
L2=$(launch_agent "$TMPDIR_AT/work-b/other-agent" "Other Agent")
SID_B=$(echo "$L2" | jget "d.get('session_id','')")
NAME_B=$(echo "$L2" | jget "d.get('name') or d.get('agent_name') or 'other-agent'")
SESS_B=$(echo "$L2" | jget "d.get('session_name') or ('claude-' + d.get('session_id',''))")
check "second dummy agent launched" '[[ -n "$SID_B" ]]' "$L2"
OUT=$(as_agent "$SESS_B" task list)
check "the second agent's list is empty" '[[ "$OUT" == "No tasks found." ]]' "$OUT"
OUT=$(as_agent "$SESS_B" task claim)
check "the second agent has nothing to claim" '[[ "$OUT" == "No available tasks" ]]' "$OUT"
set +e
OUT=$(as_agent "$SESS_B" task complete "$ID2" 2>&1); CODE=$?
set -e
check "the second agent cannot complete the first agent's task" '[[ $CODE -ne 0 ]] && echo "$OUT" | grep -q "Error completing task" && echo "$OUT" | grep -q "No such task"' "$OUT"
check "task 2 completion is unchanged" 'echo " $(task_states "$NAME_A" "$SID_A") " | grep -q " $ID2:1 "'

# ── Test 8: concurrent claims never hand out the same task twice ─────

log "Test 8: Concurrent claims..."
for i in $(seq 1 20); do create_task "$NAME_A" "$SID_A" "Batch task $i" "Batch body $i" false >/dev/null; done
PENDING=$(task_states "$NAME_A" "$SID_A" | tr ' ' '\n' | grep -c ":0$" || true)
CLAIMERS=$((PENDING + 5))
mkdir -p "$TMPDIR_AT/claims"
CLAIM_PIDS=()
for i in $(seq 1 "$CLAIMERS"); do
    ( set +e; as_agent "$SESS_A" task claim >"$TMPDIR_AT/claims/$i.out" 2>&1; echo $? >"$TMPDIR_AT/claims/$i.code" ) &
    CLAIM_PIDS+=($!)
done
# Only the claimers (a bare wait would also wait for the server)
wait "${CLAIM_PIDS[@]}" || true
CLAIMED=$(cat "$TMPDIR_AT"/claims/*.out | sed -n 's/^Claimed Task #\([0-9]*\) .*/\1/p' | sort -n)
N_CLAIMED=$(echo "$CLAIMED" | grep -c . || true)
N_UNIQUE=$(echo "$CLAIMED" | sort -u | grep -c . || true)
N_ERRORS=$(cat "$TMPDIR_AT"/claims/*.code | grep -vc "^0$" || true)
check "$CLAIMERS concurrent claims permit exactly one active task" '[[ $N_CLAIMED -eq 1 && $N_UNIQUE -eq 1 ]]' "claimed=$N_CLAIMED unique=$N_UNIQUE"
check "other concurrent claims are rejected" '[[ $N_ERRORS -eq $((CLAIMERS - 1)) ]]' "errors=$N_ERRORS"
check "remaining tasks stay pending" '[[ $(task_states "$NAME_A" "$SID_A" | tr " " "\n" | grep -c ":0$" || true) -eq $((PENDING - 1)) ]]'

# ── Test 9: finish everything ────────────────────────────────────────

log "Test 9: Complete all in-progress tasks..."
for id in $(task_states "$NAME_A" "$SID_A" | tr ' ' '\n' | sed -n 's/^\([0-9]*\):[02]$/\1/p'); do
    as_agent "$SESS_A" task complete "$id" >/dev/null
done
STATES=$(task_states "$NAME_A" "$SID_A")
TOTAL=$(echo "$STATES" | wc -w | tr -d ' ')
OPEN=$(echo "$STATES" | tr ' ' '\n' | grep -cE ":(0|2)$" || true)
CANCELLED=$(echo "$STATES" | tr ' ' '\n' | grep -c ":3$" || true)
check "all $TOTAL tasks are finished (1 cancelled, the rest done)" '[[ $OPEN -eq 0 && $CANCELLED -eq 1 && $TOTAL -eq 29 ]]' "$STATES"
OUT=$(as_agent "$SESS_A" task current)
check "current reports no active task" '[[ "$OUT" == "No active task" ]]' "$OUT"

# Exercise team workflows with the same live mock agents and real board CLI.
# #3936/#3963: agent_task_workflows drives Orchestrator and Reviewer board roles through
# coral-board, which needs a session token under strict local auth. Launch them as real agents
# (like Builder/Tester) so Coral issues their tokens; as_board then carries token_of.
LP=$(launch_agent "$TMPDIR_AT/work-planner/agent" "Task Planner")
SESS_PLANNER=$(echo "$LP" | jget "d.get('session_name') or ('claude-' + d.get('session_id',''))")
LR=$(launch_agent "$TMPDIR_AT/work-reviewer/agent" "Task Reviewer")
SESS_REVIEWER=$(echo "$LR" | jget "d.get('session_name') or ('claude-' + d.get('session_id',''))")

source "$SCRIPT_DIR/personal_task_workflows.sh"
source "$SCRIPT_DIR/agent_task_workflows.sh"

# ── Test 14: FIFO claimability honors cancellation, dependencies, and ownership ──

log "Test 14: FIFO claimability skips terminal/blocked rows and honors assigned work..."
builder join claim-regressions --as Builder >/dev/null
tester join claim-regressions --as Tester >/dev/null
planner join claim-regressions --as Orchestrator >/dev/null

CANCELLED_FIFO=$(planner task add "Cancelled FIFO row" --priority critical | task_id)
planner task cancel "$CANCELLED_FIFO" >/dev/null # unassigned: the orchestrator's to cancel
ASSIGNED_FIFO=$(planner task add "Assigned ready row" --priority low --assignee Builder | task_id)
GLOBAL_FIFO=$(planner task add "Global critical row" --priority critical | task_id)
OUT=$(builder task claim)
check "FIFO prefers the agent's assigned ready task over global priority" 'echo "$OUT" | grep -q "Claimed Task #$ASSIGNED_FIFO"'
builder task complete "$ASSIGNED_FIFO" >/dev/null
OUT=$(builder task claim)
check "FIFO skips cancelled rows and claims the remaining global task" 'echo "$OUT" | grep -q "Claimed Task #$GLOBAL_FIFO"'
builder task complete "$GLOBAL_FIFO" >/dev/null

FAILED_FIFO=$(planner task add "Failed prerequisite" --priority critical | task_id)
BLOCKED_FIFO=$(planner task add "Success-only dependent" --blocked-by "[$FAILED_FIFO]" | task_id)
builder task claim "$FAILED_FIFO" >/dev/null
builder task complete "$FAILED_FIFO" --outcome failed --message "candidate rejected" >/dev/null
READY_FIFO=$(planner task add "Independent ready row" --priority low | task_id)
set +e
OUT=$(tester task claim 2>&1)
CODE=$?
set -e
check "FIFO does not claim a task whose prerequisite failed" '[[ $CODE -eq 0 ]] && [[ "$OUT" == *"Independent ready row"* ]] && [[ "$OUT" != *"Success-only dependent"* ]]' "$OUT"
tester task complete "$READY_FIFO" >/dev/null

DIRECT_OTHER=$(planner task add "Earlier direct-ID row" --priority critical | task_id)
DIRECT_TARGET=$(planner task add "Direct-ID target" --priority low | task_id)
OUT=$(tester task claim "$DIRECT_TARGET")
check "direct-ID claim selects the requested ready task" 'echo "$OUT" | grep -q "Claimed Task #$DIRECT_TARGET"'
tester task complete "$DIRECT_TARGET" >/dev/null
DIRECT_OTHER_STATUS=$(api GET "/api/board/claim-regressions/tasks/$DIRECT_OTHER" | jget "d['status']")
check "direct-ID claim leaves other queue rows pending" '[[ "$DIRECT_OTHER_STATUS" == pending ]]'
# Keep the queue clean for the cancellation propagation case below.
OUT=$(tester task claim "$DIRECT_OTHER")
tester task complete "$DIRECT_OTHER" >/dev/null

# ── Test 14b: cancellation stalls descendants and never leaks into FIFO ───

log "Test 14b: canceled prerequisites stall descendants and FIFO skips them..."
CANCEL_ROOT=$(planner task add "Canceled prerequisite root" --priority critical | task_id)
CANCEL_CHILD=$(planner task add "Stalled dependent" --priority critical --blocked-by "[$CANCEL_ROOT]" --assignee Tester | task_id)
CANCEL_GRANDCHILD=$(planner task add "Stalled grandchild" --priority critical --blocked-by "[$CANCEL_CHILD]" --assignee Tester | task_id)
planner task cancel "$CANCEL_ROOT" --message "superseded" >/dev/null
CHILD_STATUS=$(api GET "/api/board/claim-regressions/tasks/$CANCEL_CHILD" | jget "d['status']")
GRANDCHILD_STATUS=$(api GET "/api/board/claim-regressions/tasks/$CANCEL_GRANDCHILD" | jget "d['status']")
check "canceling a prerequisite leaves the direct dependent blocked" '[[ "$CHILD_STATUS" == blocked ]]' "$CHILD_STATUS"
check "canceling a prerequisite propagates blocked state to descendants" '[[ "$GRANDCHILD_STATUS" == blocked ]]' "$GRANDCHILD_STATUS"
INDEPENDENT_AFTER_CANCEL=$(planner task add "Runnable after canceled graph" --priority low | task_id)
OUT=$(tester task claim)
check "FIFO skips assigned blocked descendants after cancellation" 'echo "$OUT" | grep -q "Runnable after canceled graph" && echo "$OUT" | grep -qv "Stalled"' "$OUT"
tester task complete "$INDEPENDENT_AFTER_CANCEL" >/dev/null
set +e
OUT=$(tester task claim "$CANCEL_CHILD" 2>&1)
CODE=$?
set -e
check "direct-ID claim refuses a stalled dependent" '[[ $CODE -ne 0 ]] && echo "$OUT" | grep -qiE "blocked|available|dependency"' "$OUT"

# Assignment ownership regressions reuse SESS_A/SESS_B under new board roles (asg_orch runs
# SESS_A as "Orchestrator"), which strict local auth refuses by design (identity_mismatch,
# #3495). They are the documented strict-off section (#3936, Security): run this runner with
# CORAL_STRICT_LOCAL_AUTH=0 to cover them (the server inherits the variable). The strict
# agent-token path is already proven by run_identity --strict, the Go e2e and Test 13c above,
# so the strict run skips this suite rather than restructure it.
case "$(printf %s "${CORAL_STRICT_LOCAL_AUTH:-on}" | tr 'A-Z' 'a-z')" in
    0|off|false|no) _assign_strict_off=1 ;;
    *)              _assign_strict_off= ;;
esac
if [[ -n "$_assign_strict_off" ]]; then
    source "$SCRIPT_DIR/agent_task_assignment.sh"
    log "Assignment editor browser regressions..."
    if CORAL_TEST_URL="$BASE_URL" bash "$SCRIPT_DIR/run_assignment_browser.sh" >"$TMPDIR_AT/assignment-browser.log" 2>&1; then
        cat "$TMPDIR_AT/assignment-browser.log"
        pass "assignment editor preserves ownership and rejects stale forms"
    else
        cat "$TMPDIR_AT/assignment-browser.log"
        fail "assignment editor browser regressions; see $TMPDIR_AT/assignment-browser.log"
    fi
else
    log "SKIP: assignment ownership + editor-browser regressions are the strict-off section (#3936); run with CORAL_STRICT_LOCAL_AUTH=0 to cover them"
fi

# ── Cleanup ──────────────────────────────────────────────────────────

# The EXIT trap cleans up mock agents and the isolated server on every path.

echo
coral_test_sandbox_check || FAIL=$((FAIL + 1))   # #4354
log "Results: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
