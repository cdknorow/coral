#!/usr/bin/env bash
#
# Stress / integration test for agent tasks (agents not on a team board).
#
# A dummy agent (mock-agent standing in for the Claude CLI) is given tasks
# from the "dashboard" API. The test checks what is typed into the agent by
# reading its terminal, then drives the agent side with the real coral-agent
# CLI: claim, current, list, complete, plus concurrent claims and isolation
# between agents.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=8474
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
PASS=0
FAIL=0
SERVER_PID=""

# ── Helpers ──────────────────────────────────────────────────────────

cleanup() {
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$TMPDIR_AT" 2>/dev/null || true
}
trap cleanup EXIT

log()  { echo "[agent-tasks] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }
check() { if eval "$2"; then pass "$1"; else fail "$1${3:+ — $3}"; fi; }

api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "$@"
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
as_agent() {
    local session_name="$1"; shift
    env -u TMUX -u CORAL_PORT CORAL_URL="$BASE_URL" CORAL_SESSION_NAME="$session_name" "$TMPDIR_AT/coral-agent" "$@"
}

# The agent's terminal as text
capture() {
    api GET "/api/sessions/live/$1/capture?agent_type=claude&session_id=$2" | jget "d.get('capture') or ''"
}

# Wait until the agent's terminal shows a line, up to ~10s. The pane wraps
# long lines, so they are joined back before searching.
wait_for_input() {
    local name="$1" sid="$2" needle="$3" i
    for i in $(seq 1 40); do
        if capture "$name" "$sid" | tr -d '\n' | grep -F -q -- "$needle"; then return 0; fi
        sleep 0.25
    done
    return 1
}

launch_agent() {
    local dir="$1" display="$2"
    mkdir -p "$dir"
    api POST "/api/sessions/launch" -d "{\"working_dir\": \"$dir\", \"agent_type\": \"claude\", \"display_name\": \"$display\"}"
}

create_task() {  # name sid title body notify
    python3 -c "import json,sys; print(json.dumps({'title': sys.argv[1], 'body': sys.argv[2], 'session_id': sys.argv[3], 'notify': sys.argv[4] == 'true'}))" "$3" "$4" "$2" "$5" \
        | curl -s -m 15 -X POST "${BASE_URL}/api/sessions/live/$1/tasks" -H "Content-Type: application/json" -d @-
}

task_states() {  # name sid -> "id:completed id:completed ..."
    api GET "/api/sessions/live/$1/tasks?session_id=$2" | jget "' '.join(f\"{t['id']}:{t['completed']}\" for t in d)"
}

# ── Setup ────────────────────────────────────────────────────────────

TMPDIR_AT="$(mktemp -d)"
export CORAL_DATA_DIR="$TMPDIR_AT/coral-data"
mkdir -p "$CORAL_DATA_DIR"

log "Building coral (dev mode), mock-agent and coral-agent..."
cd "$CORAL_DIR"
go build -tags dev -o "$TMPDIR_AT/coral" ./cmd/coral/
go build -o "$TMPDIR_AT/mock-agent" ./cmd/mock-agent/
go build -o "$TMPDIR_AT/coral-agent" ./cmd/coral-agent/

log "Starting coral server on port $PORT..."
env -u CORAL_PORT -u CORAL_URL "$TMPDIR_AT/coral" --host "$HOST" --port "$PORT" --backend tmux >"$TMPDIR_AT/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# The dummy agent stands in for the Claude CLI
api PUT "/api/settings" -d "{\"cli_path_claude\": \"$TMPDIR_AT/mock-agent\"}" >/dev/null

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
OUT=$(as_agent "$SESS_A" task claim)
check "the next claim is task 2 with its details" 'echo "$OUT" | grep -qx "Claimed Task #$ID2 (medium): Write engine tests" && echo "$OUT" | grep -q "Cover ties and forfeits."' "$OUT"
OUT=$(as_agent "$SESS_A" task complete "$ID1" --message "Logged in battle_log.go")
check "complete: Completed Task #N: title" '[[ "$OUT" == "Completed Task #$ID1: Add a battle log" ]]' "$OUT"
OUT=$(as_agent "$SESS_A" task complete "$ID1")
check "completing again is harmless" '[[ "$OUT" == "Completed Task #$ID1: Add a battle log" ]]' "$OUT"
OUT=$(as_agent "$SESS_A" task cancel "$ID4" --message "Not needed")
check "cancel: Cancelled Task #N: title" '[[ "$OUT" == "Cancelled Task #$ID4: Quiet task" ]]' "$OUT"
STATES=$(task_states "$NAME_A" "$SID_A")
check "the dashboard sees done / in progress / pending / cancelled" 'echo " $STATES " | grep -q " $ID1:1 " && echo " $STATES " | grep -q " $ID2:2 " && echo " $STATES " | grep -q " $ID3:0 " && echo " $STATES " | grep -q " $ID4:3 "' "$STATES"
OUT=$(as_agent "$SESS_A" task list)
check "list shows completed, in_progress, pending and skipped" 'echo "$OUT" | grep -qE "^#$ID1 +completed " && echo "$OUT" | grep -qE "^#$ID2 +in_progress " && echo "$OUT" | grep -qE "^#$ID3 +pending " && echo "$OUT" | grep -qE "^#$ID4 +skipped "' "$OUT"
MSG=$(api GET "/api/agent/tasks?session_id=$SID_A" | jget "next(t.get('completion_message','') for t in d['tasks'] if t['id']==$ID1)")
check "the completion message is stored" '[[ "$MSG" == "Logged in battle_log.go" ]]' "$MSG"

# ── Test 6: claim order follows priority, like a board ───────────────

log "Test 6: Claim order is by priority, then age..."
for p in low critical medium high; do as_agent "$SESS_A" task add "Priority $p" --priority "$p" >/dev/null; done
OUT=$(as_agent "$SESS_A" task add "Added by the agent" --body "Self-assigned details" --priority low)
check "task add: Created Task #N: title" 'echo "$OUT" | grep -qE "^Created Task #[0-9]+: Added by the agent$"' "$OUT"
ORDER=""
for i in 1 2 3 4 5; do
    ORDER="$ORDER|$(as_agent "$SESS_A" task claim | head -1 | sed 's/^Claimed Task #[0-9]* (\([a-z]*\)): \(.*\)/\2/')"
done
# Pending before: task 3 (medium, older), then the new ones
check "claims go critical, high, medium (oldest first), low (oldest first)" '[[ "$ORDER" == "|Priority critical|Priority high|Fix the login form|Priority medium|Priority low" ]]' "$ORDER"
OUT=$(as_agent "$SESS_A" task claim)
check "the agent-added task (low, newest) comes last, with its details" 'echo "$OUT" | grep -qx "Claimed Task #[0-9]* (low): Added by the agent" && echo "$OUT" | grep -q "Self-assigned details"' "$OUT"
OUT=$(as_agent "$SESS_A" task claim)
check "with nothing available: No available tasks (exit 0, as coral-board)" '[[ "$OUT" == "No available tasks" ]]' "$OUT"

# ── Test 7: a second agent is isolated ───────────────────────────────

log "Test 7: A second agent cannot see or touch the first agent's tasks..."
L2=$(launch_agent "$TMPDIR_AT/work-b/other-agent" "Other Agent")
SID_B=$(echo "$L2" | jget "d.get('session_id','')")
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
check "task 2 is still in progress" 'echo " $(task_states "$NAME_A" "$SID_A") " | grep -q " $ID2:2 "'

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
N_EMPTY=$(grep -lx "No available tasks" "$TMPDIR_AT"/claims/*.out 2>/dev/null | wc -l | tr -d ' ')
N_ERRORS=$(cat "$TMPDIR_AT"/claims/*.code | grep -vc "^0$" || true)
check "$CLAIMERS concurrent claims on $PENDING pending tasks: each task claimed once" '[[ $N_CLAIMED -eq $PENDING && $N_UNIQUE -eq $PENDING ]]' "claimed=$N_CLAIMED unique=$N_UNIQUE"
check "the extra claimers are told nothing is available, and none errored" '[[ $N_EMPTY -eq 5 && $N_ERRORS -eq 0 ]]' "empty=$N_EMPTY errors=$N_ERRORS"
check "no task is left pending" '[[ $(task_states "$NAME_A" "$SID_A" | tr " " "\n" | grep -c ":0$" || true) -eq 0 ]]'

# ── Test 9: finish everything ────────────────────────────────────────

log "Test 9: Complete all in-progress tasks..."
for id in $(task_states "$NAME_A" "$SID_A" | tr ' ' '\n' | sed -n 's/^\([0-9]*\):2$/\1/p'); do
    as_agent "$SESS_A" task complete "$id" >/dev/null
done
STATES=$(task_states "$NAME_A" "$SID_A")
TOTAL=$(echo "$STATES" | wc -w | tr -d ' ')
OPEN=$(echo "$STATES" | tr ' ' '\n' | grep -cE ":(0|2)$" || true)
CANCELLED=$(echo "$STATES" | tr ' ' '\n' | grep -c ":3$" || true)
check "all $TOTAL tasks are finished (1 cancelled, the rest done)" '[[ $OPEN -eq 0 && $CANCELLED -eq 1 && $TOTAL -eq 29 ]]' "$STATES"
OUT=$(as_agent "$SESS_A" task current)
check "current reports no active task" '[[ "$OUT" == "No active task" ]]' "$OUT"

# ── Cleanup ──────────────────────────────────────────────────────────

api POST "/api/sessions/live/$NAME_A/kill" -d "{\"agent_type\": \"claude\", \"session_id\": \"$SID_A\"}" >/dev/null || true
[[ -n "$SID_B" ]] && api POST "/api/sessions/live/other-agent/kill" -d "{\"agent_type\": \"claude\", \"session_id\": \"$SID_B\"}" >/dev/null || true

echo
log "Results: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
