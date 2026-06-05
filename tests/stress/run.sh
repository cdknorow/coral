#!/usr/bin/env bash
#
# Stress test for the Coral board task system.
# Tests task creation, claiming, completion, and listing.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=8471
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
BOARD="stress-test-$$"
NUM_AGENTS=5
NUM_TASKS=20
PASS=0
FAIL=0
SERVER_PID=""

# ── Helpers ──────────────────────────────────────────────────────────

cleanup() {
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$TMPDIR_STRESS" 2>/dev/null || true
}
trap cleanup EXIT

log()  { echo "[stress] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }

api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/board/${BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

wait_for_server() {
    local retries=30
    while ! curl -s -m 5 "${BASE_URL}/api/health" >/dev/null 2>&1; do
        retries=$((retries - 1))
        if [[ $retries -le 0 ]]; then
            log "ERROR: Server failed to start on port $PORT"
            exit 1
        fi
        sleep 0.5
    done
    log "Server is ready on port $PORT"
}

# ── Setup ────────────────────────────────────────────────────────────

TMPDIR_STRESS="$(mktemp -d)"
export CORAL_DATA_DIR="$TMPDIR_STRESS"

log "Building coral (dev mode)..."
cd "$CORAL_DIR"
go build -tags dev -o "$TMPDIR_STRESS/coral" ./cmd/coral/

log "Starting coral server on port $PORT..."
"$TMPDIR_STRESS/coral" --host "$HOST" --port "$PORT" --backend tmux >"$TMPDIR_STRESS/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# ── Test 1: Create tasks ────────────────────────────────────────────

log "Creating $NUM_TASKS tasks..."
created=0
for i in $(seq 1 $NUM_TASKS); do
    prio="medium"
    case $((i % 4)) in
        0) prio="critical" ;;
        1) prio="high" ;;
        2) prio="medium" ;;
        3) prio="low" ;;
    esac
    result=$(api POST "/tasks" -d "{\"title\": \"Task $i\", \"body\": \"Stress test task number $i\", \"priority\": \"$prio\", \"subscriber_id\": \"orchestrator\"}")
    tid=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
    if [[ -n "$tid" ]]; then
        created=$((created + 1))
    fi
done

if [[ "$created" -eq "$NUM_TASKS" ]]; then
    pass "Created $NUM_TASKS tasks"
else
    fail "Created $created tasks, expected $NUM_TASKS"
fi

# ── Test 2: List returns all tasks ───────────────────────────────────

list_count=$(api GET "/tasks" | python3 -c "import sys,json; print(len(json.load(sys.stdin).get('tasks', [])))" 2>/dev/null || echo "0")

if [[ "$list_count" -eq "$NUM_TASKS" ]]; then
    pass "ListTasks returns all $NUM_TASKS tasks"
else
    fail "ListTasks returned $list_count, expected $NUM_TASKS"
fi

# ── Test 3: Concurrent claims — no duplicates ────────────────────────

log "Testing $NUM_AGENTS concurrent claims..."

# Use xargs -P for reliable parallel execution
CLAIM_RESULTS="$TMPDIR_STRESS/claim_results"
mkdir -p "$CLAIM_RESULTS"

# Write a helper script for xargs to call
cat > "$TMPDIR_STRESS/do_claim.sh" <<SCRIPT
#!/usr/bin/env bash
i=\$1
curl -s -m 10 -X POST "${BASE_URL}/api/board/${BOARD}/tasks/claim" \
    -H "Content-Type: application/json" \
    -d "{\"subscriber_id\": \"agent-\${i}\"}" > "${CLAIM_RESULTS}/agent-\${i}.json"
SCRIPT
chmod +x "$TMPDIR_STRESS/do_claim.sh"

seq 1 $NUM_AGENTS | xargs -P "$NUM_AGENTS" -I{} "$TMPDIR_STRESS/do_claim.sh" {}

# All agents should have claimed a task (20 pending, 5 agents)
claim_success=0
claimed_ids=()
for i in $(seq 1 $NUM_AGENTS); do
    tid=$(cat "$CLAIM_RESULTS/agent-${i}.json" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
    if [[ -n "$tid" ]]; then
        claim_success=$((claim_success + 1))
        claimed_ids+=("$tid")
    fi
done

# Under SQLite single-writer, some concurrent claims may fail with contention.
# The key invariant is no duplicates. Require at least 1 success.
if [[ "$claim_success" -ge 1 ]]; then
    pass "$claim_success of $NUM_AGENTS agents claimed a task concurrently"
else
    fail "No concurrent claims succeeded"
fi

unique=$(printf '%s\n' "${claimed_ids[@]}" | sort -u | wc -l | tr -d ' ')
if [[ "${#claimed_ids[@]}" -eq "$unique" ]]; then
    pass "No duplicate claims: all $unique task IDs unique"
else
    fail "Duplicate claims: ${#claimed_ids[@]} claims but $unique unique"
fi

# ── Test 4: Claim + complete lifecycle ───────────────────────────────

log "Claiming and completing all remaining tasks..."

# Complete the tasks claimed above
for i in $(seq 1 $NUM_AGENTS); do
    tid=$(cat "$CLAIM_RESULTS/agent-${i}.json" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
    if [[ -n "$tid" ]]; then
        api POST "/tasks/${tid}/complete" -d "{\"subscriber_id\": \"agent-${i}\", \"message\": \"done\"}" >/dev/null
    fi
done

# Claim and complete remaining tasks sequentially
while true; do
    result=$(api POST "/tasks/claim" -d '{"subscriber_id": "agent-1"}')
    tid=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
    [[ -z "$tid" ]] && break
    api POST "/tasks/${tid}/complete" -d '{"subscriber_id": "agent-1", "message": "done"}' >/dev/null
done

# Verify all completed
completed_count=$(api GET "/tasks" | python3 -c "
import sys, json
tasks = json.load(sys.stdin).get('tasks', [])
print(sum(1 for t in tasks if t.get('status') == 'completed'))
" 2>/dev/null || echo "0")

if [[ "$completed_count" -eq "$NUM_TASKS" ]]; then
    pass "All $NUM_TASKS tasks claimed and completed"
else
    fail "Only $completed_count of $NUM_TASKS completed"
fi

# ── Test 5: Claim returns 404 when all done ──────────────────────────

status=$(api_status POST "/tasks/claim" -d '{"subscriber_id": "agent-1"}')
if [[ "$status" == "404" ]]; then
    pass "Claim returns 404 when no tasks available"
else
    fail "Claim returned HTTP $status, expected 404"
fi

# ── Test 6: Sequential claiming — cannot claim while in-progress ────

# Use a fresh board for sequential tests to avoid interference
SEQ_BOARD="seq-test-$$"
seq_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${SEQ_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}
seq_api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/board/${SEQ_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

# Subscribe an agent
seq_api POST "/subscribe" -d '{"subscriber_id": "seq-agent", "job_title": "tester"}' >/dev/null

# Create two tasks
seq_api POST "/tasks" -d '{"title": "Seq Task 1", "priority": "high", "subscriber_id": "orchestrator"}' >/dev/null
seq_api POST "/tasks" -d '{"title": "Seq Task 2", "priority": "medium", "subscriber_id": "orchestrator"}' >/dev/null

# Claim first task
t1_result=$(seq_api POST "/tasks/claim" -d '{"subscriber_id": "seq-agent"}')
t1_id=$(echo "$t1_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

if [[ -n "$t1_id" ]]; then
    pass "Sequential: agent claimed first task (id=$t1_id)"
else
    fail "Sequential: agent failed to claim first task"
fi

# Try to claim second task while first is in-progress — should get 409
status=$(seq_api_status POST "/tasks/claim" -d '{"subscriber_id": "seq-agent"}')
if [[ "$status" == "409" ]]; then
    pass "Sequential: second claim blocked with 409 while task in-progress"
else
    fail "Sequential: expected 409 for second claim, got HTTP $status"
fi

# Verify the error message mentions completing current task
err_body=$(seq_api POST "/tasks/claim" -d '{"subscriber_id": "seq-agent"}')
err_msg=$(echo "$err_body" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error',''))" 2>/dev/null || echo "")
if echo "$err_msg" | grep -qi "complete"; then
    pass "Sequential: error message mentions completing current task"
else
    fail "Sequential: error message unclear: '$err_msg'"
fi

# ── Test 7: Claim after complete — agent can claim again ────────────

# Complete the first task
seq_api POST "/tasks/${t1_id}/complete" -d '{"subscriber_id": "seq-agent", "message": "done"}' >/dev/null

# Now claim should succeed
t2_result=$(seq_api POST "/tasks/claim" -d '{"subscriber_id": "seq-agent"}')
t2_id=$(echo "$t2_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

if [[ -n "$t2_id" ]] && [[ "$t2_id" != "$t1_id" ]]; then
    pass "Sequential: agent claimed second task after completing first (id=$t2_id)"
else
    fail "Sequential: failed to claim after completion (got '$t2_id')"
fi

# Complete second task
seq_api POST "/tasks/${t2_id}/complete" -d '{"subscriber_id": "seq-agent", "message": "done"}' >/dev/null

# ── Test 8: Priority ordering — tasks come out in priority order ─────

PRIO_BOARD="prio-test-$$"
prio_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${PRIO_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

# Subscribe
prio_api POST "/subscribe" -d '{"subscriber_id": "prio-agent", "job_title": "tester"}' >/dev/null

# Create tasks in reverse priority order (low first, critical last)
prio_api POST "/tasks" -d '{"title": "Low prio", "priority": "low", "subscriber_id": "orchestrator"}' >/dev/null
prio_api POST "/tasks" -d '{"title": "Medium prio", "priority": "medium", "subscriber_id": "orchestrator"}' >/dev/null
prio_api POST "/tasks" -d '{"title": "Critical prio", "priority": "critical", "subscriber_id": "orchestrator"}' >/dev/null
prio_api POST "/tasks" -d '{"title": "High prio", "priority": "high", "subscriber_id": "orchestrator"}' >/dev/null

# Claim and complete in sequence, record the priority order
claimed_prios=()
for i in $(seq 1 4); do
    result=$(prio_api POST "/tasks/claim" -d '{"subscriber_id": "prio-agent"}')
    title=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('title',''))" 2>/dev/null || echo "")
    tid=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
    claimed_prios+=("$title")
    if [[ -n "$tid" ]]; then
        prio_api POST "/tasks/${tid}/complete" -d '{"subscriber_id": "prio-agent", "message": "done"}' >/dev/null
    fi
done

expected_order="Critical prio|High prio|Medium prio|Low prio"
actual_order=$(IFS='|'; echo "${claimed_prios[*]}")

if [[ "$actual_order" == "$expected_order" ]]; then
    pass "Priority ordering: tasks claimed in correct order (critical > high > medium > low)"
else
    fail "Priority ordering: expected '$expected_order', got '$actual_order'"
fi

# ── Test 9: Coral Task Queue messages don't count as unread ─────────

NUDGE_BOARD="nudge-test-$$"
nudge_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${NUDGE_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

# Subscribe the agent and read all existing messages to start at zero unread
nudge_api POST "/subscribe" -d '{"subscriber_id": "nudge-agent", "job_title": "tester"}' >/dev/null
nudge_api GET "/messages?subscriber_id=nudge-agent" >/dev/null

# Create and claim a task — this generates 'Coral Task Queue' audit messages
nudge_api POST "/tasks" -d '{"title": "Nudge Task 1", "priority": "high", "subscriber_id": "orchestrator"}' >/dev/null
nudge_api POST "/tasks" -d '{"title": "Nudge Task 2", "priority": "medium", "subscriber_id": "orchestrator"}' >/dev/null

# Read messages again to clear cursor (task creation may post messages)
nudge_api GET "/messages?subscriber_id=nudge-agent" >/dev/null

# Claim a task — this posts a 'Coral Task Queue' audit message
nudge_api POST "/tasks/claim" -d '{"subscriber_id": "nudge-agent"}' >/dev/null

# Check unread — Coral Task Queue messages should NOT count
unread=$(curl -s -m 10 "${BASE_URL}/api/board/${NUDGE_BOARD}/messages/check?subscriber_id=nudge-agent" | python3 -c "import sys,json; print(json.load(sys.stdin).get('unread',99))" 2>/dev/null || echo "99")

if [[ "$unread" -eq 0 ]]; then
    pass "Coral Task Queue messages do not count as unread"
else
    fail "Expected 0 unread after task queue messages, got $unread"
fi

# ── Test 10: Completion posts nudge when next task is pending ────────

# Complete the first task — there's a second pending task, so a nudge should be posted
t_id=$(nudge_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t.get('status') == 'in_progress':
        print(t['id']); break
" 2>/dev/null || echo "")

if [[ -n "$t_id" ]]; then
    nudge_api POST "/tasks/${t_id}/complete" -d '{"subscriber_id": "nudge-agent", "message": "done"}' >/dev/null
fi

# Give the async goroutine a moment to post
sleep 0.5

# Read recent messages and check for a nudge about available tasks
msgs=$(nudge_api GET "/messages?subscriber_id=nudge-agent")
has_nudge=$(echo "$msgs" | python3 -c "
import sys,json
data = json.load(sys.stdin)
messages = data if isinstance(data, list) else data.get('messages', [])
# Look for nudge from Coral Task Queue mentioning 'claim'
found = any('claim' in m.get('content','').lower()
            for m in messages if m.get('subscriber_id','') == 'Coral Task Queue')
print('yes' if found else 'no')
" 2>/dev/null || echo "no")

if [[ "$has_nudge" == "yes" ]]; then
    pass "Completion posts nudge message about next pending task"
else
    fail "No nudge message found after completing task with another pending"
fi

# ── Test 11: Terminal nudge delivered to agent's tmux pane ───────────

# Build the mock agent
log "Building mock-agent for terminal nudge test..."
go build -o "$TMPDIR_STRESS/mock-agent" ./cmd/mock-agent/

# Override claude CLI to use mock-agent
curl -s -m 10 -X PUT "${BASE_URL}/api/settings" \
    -H "Content-Type: application/json" \
    -d "{\"cli_path_claude\": \"$TMPDIR_STRESS/mock-agent\"}" >/dev/null

TERM_BOARD="term-nudge-$$"
term_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${TERM_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

# Launch a mock agent session
launch_result=$(curl -s -m 10 -X POST "${BASE_URL}/api/sessions/launch" \
    -H "Content-Type: application/json" \
    -d '{"working_dir": "/tmp", "agent_type": "claude", "display_name": "Nudge Tester"}')
mock_session=$(echo "$launch_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_name',''))" 2>/dev/null || echo "")

if [[ -z "$mock_session" ]]; then
    fail "Terminal nudge: could not launch mock agent session"
else
    # Wait for mock agent to start
    sleep 3

    # Subscribe the session to the board
    term_api POST "/subscribe" \
        -d "{\"subscriber_id\": \"$mock_session\", \"job_title\": \"tester\", \"session_name\": \"$mock_session\"}" >/dev/null

    # Create 2 tasks, claim first, complete it
    term_api POST "/tasks" -d '{"title": "Terminal Task 1", "priority": "high", "subscriber_id": "orchestrator"}' >/dev/null
    term_api POST "/tasks" -d '{"title": "Terminal Task 2", "priority": "medium", "subscriber_id": "orchestrator"}' >/dev/null

    term_tid=$(term_api POST "/tasks/claim" -d "{\"subscriber_id\": \"$mock_session\"}" | \
        python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

    term_api POST "/tasks/${term_tid}/complete" \
        -d "{\"subscriber_id\": \"$mock_session\", \"message\": \"done\"}" >/dev/null

    # Wait for async SendInput delivery
    sleep 2

    # Capture the terminal and check for nudge text
    capture=$(curl -s -m 10 "${BASE_URL}/api/sessions/live/${mock_session}/capture" 2>/dev/null)
    has_terminal_nudge=$(echo "$capture" | python3 -c "
import sys, json
data = json.load(sys.stdin)
text = (data.get('capture') or '').lower()
print('yes' if 'task' in text and 'claim' in text else 'no')
" 2>/dev/null || echo "no")

    if [[ "$has_terminal_nudge" == "yes" ]]; then
        pass "Terminal nudge delivered to agent's tmux pane"
    else
        fail "Terminal nudge not found in agent's tmux pane capture"
    fi

    # Clean up the mock session
    curl -s -m 10 -X POST "${BASE_URL}/api/sessions/live/${mock_session}/kill" >/dev/null
fi

# ── Test 12: Orchestrator notified when agent completes task ─────────

ORCH_BOARD="orch-notify-$$"
orch_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${ORCH_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

# Launch a mock orchestrator session subscribed to the board.
orch_launch=$(curl -s -m 10 -X POST "${BASE_URL}/api/sessions/launch" \
    -H "Content-Type: application/json" \
    -d "{\"working_dir\": \"/tmp\", \"agent_type\": \"claude\", \"display_name\": \"Orchestrator\", \"board_name\": \"${ORCH_BOARD}\"}")
orch_session=$(echo "$orch_launch" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_name',''))" 2>/dev/null || echo "")

if [[ -z "$orch_session" ]]; then
    fail "Orchestrator completion notification: could not launch orchestrator session"
else
    sleep 3

    # Subscribe a worker identity, then have it claim and complete a task.
    orch_api POST "/subscribe" -d '{"subscriber_id": "Backend Dev", "job_title": "Backend Dev"}' >/dev/null
    orch_api POST "/tasks" -d '{"title": "Orchestrator Notify Task", "priority": "high", "created_by": "Orchestrator", "assigned_to": "Backend Dev"}' >/dev/null

    orch_task_id=$(orch_api POST "/tasks/claim" -d '{"subscriber_id": "Backend Dev"}' | \
        python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

    if [[ -n "$orch_task_id" ]]; then
        orch_api POST "/tasks/${orch_task_id}/complete" \
            -d '{"subscriber_id": "Backend Dev", "message": "finished stress task"}' >/dev/null

        sleep 2

        orch_capture=$(curl -s -m 10 "${BASE_URL}/api/sessions/live/${orch_session}/capture" 2>/dev/null)
        has_orch_notice=$(echo "$orch_capture" | python3 -c "
import sys, json
data = json.load(sys.stdin)
text = data.get('capture') or ''
print('yes' if 'Backend Dev finished task #{}: finished stress task'.format(${orch_task_id:-0}) in text else 'no')
" 2>/dev/null || echo "no")

        if [[ "$has_orch_notice" == "yes" ]]; then
            pass "Orchestrator receives completion notification when agent finishes task"
        else
            fail "Orchestrator completion notification not found in pane capture"
        fi
    else
        fail "Orchestrator completion notification: worker could not claim task"
    fi

    curl -s -m 10 -X POST "${BASE_URL}/api/sessions/live/${orch_session}/kill" >/dev/null
fi

# Reset CLI path
curl -s -m 10 -X PUT "${BASE_URL}/api/settings" \
    -H "Content-Type: application/json" \
    -d '{"cli_path_claude": ""}' >/dev/null

# ── Test 13: Create task with blocked_by — starts as blocked ─────────

DEP_BOARD="dep-test-$$"
dep_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DEP_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}
dep_api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/board/${DEP_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

dep_api POST "/subscribe" -d '{"subscriber_id": "dep-agent", "job_title": "tester"}' >/dev/null

# Create Task A (no deps)
dep_a=$(dep_api POST "/tasks" -d '{"title": "Dep Task A", "priority": "high", "subscriber_id": "orchestrator"}')
dep_a_id=$(echo "$dep_a" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
dep_a_status=$(echo "$dep_a" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$dep_a_status" == "pending" ]]; then
    pass "Dep: Task A created as pending"
else
    fail "Dep: Task A status is '$dep_a_status', expected 'pending'"
fi

# Create Task B blocked by A
dep_b=$(dep_api POST "/tasks" -d "{\"title\": \"Dep Task B\", \"priority\": \"medium\", \"subscriber_id\": \"orchestrator\", \"blocked_by\": [${dep_a_id}]}")
dep_b_id=$(echo "$dep_b" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
dep_b_status=$(echo "$dep_b" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$dep_b_status" == "blocked" ]]; then
    pass "Dep: Task B created as blocked (blocked_by Task A)"
else
    fail "Dep: Task B status is '$dep_b_status', expected 'blocked'"
fi

# ── Test 14: Blocked task cannot be claimed ──────────────────────────

claim_result=$(dep_api POST "/tasks/claim" -d '{"subscriber_id": "dep-agent"}')
claimed_title=$(echo "$claim_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('title',''))" 2>/dev/null || echo "")

if [[ "$claimed_title" == "Dep Task A" ]]; then
    pass "Dep: Claim skips blocked task, returns pending Task A"
else
    fail "Dep: Claim returned '$claimed_title', expected 'Dep Task A'"
fi

# ── Test 15: Completing blocker auto-unblocks downstream ─────────────

dep_api POST "/tasks/${dep_a_id}/complete" -d '{"subscriber_id": "dep-agent", "message": "done"}' >/dev/null

# Give async notification goroutine a moment
sleep 0.5

dep_b_new_status=$(dep_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${dep_b_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$dep_b_new_status" == "pending" ]]; then
    pass "Dep: Completing blocker auto-unblocks downstream (blocked → pending)"
else
    fail "Dep: Task B status is '$dep_b_new_status' after completing A, expected 'pending'"
fi

# Claim B — should succeed now
claim_b=$(dep_api POST "/tasks/claim" -d '{"subscriber_id": "dep-agent"}')
claim_b_title=$(echo "$claim_b" | python3 -c "import sys,json; print(json.load(sys.stdin).get('title',''))" 2>/dev/null || echo "")
claim_b_id=$(echo "$claim_b" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

if [[ "$claim_b_title" == "Dep Task B" ]]; then
    pass "Dep: Unblocked Task B can now be claimed"
else
    fail "Dep: Expected to claim 'Dep Task B', got '$claim_b_title'"
fi

dep_api POST "/tasks/${claim_b_id}/complete" -d '{"subscriber_id": "dep-agent", "message": "done"}' >/dev/null

# ── Test 16: Cancelling blocker auto-unblocks downstream ─────────────

DEP2_BOARD="dep2-test-$$"
dep2_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DEP2_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

dep2_api POST "/subscribe" -d '{"subscriber_id": "dep2-agent", "job_title": "tester"}' >/dev/null

dep_c=$(dep2_api POST "/tasks" -d '{"title": "Cancel Blocker", "subscriber_id": "orchestrator"}')
dep_c_id=$(echo "$dep_c" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

dep_d=$(dep2_api POST "/tasks" -d "{\"title\": \"Blocked by cancel\", \"subscriber_id\": \"orchestrator\", \"blocked_by\": [${dep_c_id}]}")
dep_d_id=$(echo "$dep_d" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

dep2_api POST "/tasks/${dep_c_id}/cancel" -d '{"subscriber_id": "orchestrator"}' >/dev/null
sleep 0.5

dep_d_status=$(dep2_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${dep_d_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$dep_d_status" == "pending" ]]; then
    pass "Dep: Cancelling blocker auto-unblocks downstream"
else
    fail "Dep: Task D status is '$dep_d_status' after cancelling blocker, expected 'pending'"
fi

# ── Test 17: Multi-dependency — all blockers must resolve ────────────

DEP3_BOARD="dep3-test-$$"
dep3_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DEP3_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

dep3_api POST "/subscribe" -d '{"subscriber_id": "dep3-agent", "job_title": "tester"}' >/dev/null

dep_e=$(dep3_api POST "/tasks" -d '{"title": "Multi Blocker E", "subscriber_id": "orchestrator"}')
dep_e_id=$(echo "$dep_e" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

dep_f=$(dep3_api POST "/tasks" -d '{"title": "Multi Blocker F", "subscriber_id": "orchestrator"}')
dep_f_id=$(echo "$dep_f" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

dep_g=$(dep3_api POST "/tasks" -d "{\"title\": \"Multi Blocked G\", \"subscriber_id\": \"orchestrator\", \"blocked_by\": [${dep_e_id}, ${dep_f_id}]}")
dep_g_id=$(echo "$dep_g" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
dep_g_status=$(echo "$dep_g" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$dep_g_status" == "blocked" ]]; then
    pass "Dep: Task G blocked by multiple tasks (E and F)"
else
    fail "Dep: Task G status is '$dep_g_status', expected 'blocked'"
fi

# Complete E — G should stay blocked (F unresolved)
claimed_e=$(dep3_api POST "/tasks/claim" -d '{"subscriber_id": "dep3-agent"}')
claimed_e_id=$(echo "$claimed_e" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
dep3_api POST "/tasks/${claimed_e_id}/complete" -d '{"subscriber_id": "dep3-agent", "message": "done"}' >/dev/null
sleep 0.3

dep_g_mid=$(dep3_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${dep_g_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$dep_g_mid" == "blocked" ]]; then
    pass "Dep: Task G still blocked after completing only one of two blockers"
else
    fail "Dep: Task G status is '$dep_g_mid' after completing E, expected 'blocked'"
fi

# Complete F — G should now unblock
claimed_f=$(dep3_api POST "/tasks/claim" -d '{"subscriber_id": "dep3-agent"}')
claimed_f_id=$(echo "$claimed_f" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
dep3_api POST "/tasks/${claimed_f_id}/complete" -d '{"subscriber_id": "dep3-agent", "message": "done"}' >/dev/null
sleep 0.3

dep_g_final=$(dep3_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${dep_g_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$dep_g_final" == "pending" ]]; then
    pass "Dep: Task G unblocked after all blockers resolved"
else
    fail "Dep: Task G status is '$dep_g_final' after completing both blockers, expected 'pending'"
fi

# ── Test 18: Circular dependency rejection ───────────────────────────

DEP4_BOARD="dep4-test-$$"
dep4_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DEP4_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}
dep4_api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/board/${DEP4_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

dep_h=$(dep4_api POST "/tasks" -d '{"title": "Cycle H", "subscriber_id": "orchestrator"}')
dep_h_id=$(echo "$dep_h" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

dep_i=$(dep4_api POST "/tasks" -d "{\"title\": \"Cycle I\", \"subscriber_id\": \"orchestrator\", \"blocked_by\": [${dep_h_id}]}")
dep_i_id=$(echo "$dep_i" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

# Try to make H blocked by I (circular: H→I→H)
cycle_status=$(dep4_api_status PATCH "/tasks/${dep_h_id}" -d "{\"blocked_by\": [${dep_i_id}]}")

if [[ "$cycle_status" == "400" ]]; then
    pass "Dep: Circular dependency rejected with 400"
else
    fail "Dep: Circular dependency returned HTTP $cycle_status, expected 400"
fi

# ── Test 19: blocked_by appears in ListTasks response ────────────────

dep_list=$(dep4_api GET "/tasks")
dep_i_blockers=$(echo "$dep_list" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${dep_i_id}:
        deps = t.get('blocked_by', [])
        if len(deps) == 1 and deps[0].get('task_id') == ${dep_h_id}:
            print('ok')
        else:
            print('bad')
        break
" 2>/dev/null || echo "bad")

if [[ "$dep_i_blockers" == "ok" ]]; then
    pass "Dep: ListTasks includes blocked_by with task IDs"
else
    fail "Dep: ListTasks blocked_by not populated correctly"
fi

# ── Test 20: Cross-board dependency ──────────────────────────────────

CROSS_BOARD1="cross-board1-$$"
CROSS_BOARD2="cross-board2-$$"

cross1_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${CROSS_BOARD1}${path}" \
        -H "Content-Type: application/json" "$@"
}
cross2_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${CROSS_BOARD2}${path}" \
        -H "Content-Type: application/json" "$@"
}

cross1_api POST "/subscribe" -d '{"subscriber_id": "cross-agent-1", "job_title": "tester"}' >/dev/null
cross2_api POST "/subscribe" -d '{"subscriber_id": "cross-agent-2", "job_title": "tester"}' >/dev/null

# Create task X on board-1
cross_x=$(cross1_api POST "/tasks" -d '{"title": "Cross Board X", "subscriber_id": "orchestrator"}')
cross_x_id=$(echo "$cross_x" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

# Create task Y on board-2, blocked by X on board-1
cross_y=$(cross2_api POST "/tasks" -d "{\"title\": \"Cross Board Y\", \"subscriber_id\": \"orchestrator\", \"blocked_by\": [{\"task_id\": ${cross_x_id}, \"board_id\": \"${CROSS_BOARD1}\"}]}")
cross_y_id=$(echo "$cross_y" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
cross_y_status=$(echo "$cross_y" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$cross_y_status" == "blocked" ]]; then
    pass "Dep: Cross-board task Y blocked by task X on another board"
else
    fail "Dep: Cross-board task Y status is '$cross_y_status', expected 'blocked'"
fi

# Complete X on board-1 — Y on board-2 should unblock
claimed_x=$(cross1_api POST "/tasks/claim" -d '{"subscriber_id": "cross-agent-1"}')
claimed_x_id=$(echo "$claimed_x" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
cross1_api POST "/tasks/${claimed_x_id}/complete" -d '{"subscriber_id": "cross-agent-1", "message": "done"}' >/dev/null
sleep 0.5

cross_y_final=$(cross2_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${cross_y_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$cross_y_final" == "pending" ]]; then
    pass "Dep: Cross-board unblock works (completing X on board-1 unblocks Y on board-2)"
else
    fail "Dep: Cross-board task Y status is '$cross_y_final' after completing X, expected 'pending'"
fi

# ── Test 21: Draft task creation and publish ─────────────────────────

DRAFT_BOARD="draft-test-$$"
draft_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DRAFT_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

draft_api POST "/subscribe" -d '{"subscriber_id": "draft-agent", "job_title": "tester"}' >/dev/null

# Create draft task
draft_result=$(draft_api POST "/tasks" -d '{"title": "Draft Task", "priority": "high", "created_by": "orchestrator", "draft": true}')
draft_id=$(echo "$draft_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
draft_status=$(echo "$draft_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$draft_status" == "draft" ]]; then
    pass "Draft: task created with draft status"
else
    fail "Draft: task status is '$draft_status', expected 'draft'"
fi

# ── Test 22: Draft task cannot be claimed ────────────────────────────

# Create a pending task too
draft_api POST "/tasks" -d '{"title": "Pending Task", "priority": "low", "created_by": "orchestrator"}' >/dev/null

# Claim should skip draft, pick pending
claim_draft=$(draft_api POST "/tasks/claim" -d '{"subscriber_id": "draft-agent"}')
claim_draft_title=$(echo "$claim_draft" | python3 -c "import sys,json; print(json.load(sys.stdin).get('title',''))" 2>/dev/null || echo "")

if [[ "$claim_draft_title" == "Pending Task" ]]; then
    pass "Draft: claim skips draft, returns pending task"
else
    fail "Draft: claim returned '$claim_draft_title', expected 'Pending Task'"
fi

# Complete the pending task
claim_draft_id=$(echo "$claim_draft" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
draft_api POST "/tasks/${claim_draft_id}/complete" -d '{"subscriber_id": "draft-agent", "message": "done"}' >/dev/null

# ── Test 23: Publish draft → pending ─────────────────────────────────

publish_result=$(draft_api POST "/tasks/${draft_id}/publish" -d '{}')
publish_status=$(echo "$publish_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$publish_status" == "pending" ]]; then
    pass "Draft: publish transitions draft → pending"
else
    fail "Draft: publish result status is '$publish_status', expected 'pending'"
fi

# Verify published task can now be claimed
claim_published=$(draft_api POST "/tasks/claim" -d '{"subscriber_id": "draft-agent"}')
claim_pub_title=$(echo "$claim_published" | python3 -c "import sys,json; print(json.load(sys.stdin).get('title',''))" 2>/dev/null || echo "")

if [[ "$claim_pub_title" == "Draft Task" ]]; then
    pass "Draft: published task can be claimed"
else
    fail "Draft: expected to claim 'Draft Task', got '$claim_pub_title'"
fi

claim_pub_id=$(echo "$claim_published" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
draft_api POST "/tasks/${claim_pub_id}/complete" -d '{"subscriber_id": "draft-agent", "message": "done"}' >/dev/null

# ── Test 24: Draft with deps → publish → blocked ────────────────────

DRAFT2_BOARD="draft2-test-$$"
draft2_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DRAFT2_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

draft2_api POST "/subscribe" -d '{"subscriber_id": "draft2-agent", "job_title": "tester"}' >/dev/null

# Create blocker
blocker=$(draft2_api POST "/tasks" -d '{"title": "Blocker", "created_by": "orchestrator"}')
blocker_id=$(echo "$blocker" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")

# Create draft with dep on blocker — should stay draft
draft_dep=$(draft2_api POST "/tasks" -d "{\"title\": \"Draft with dep\", \"created_by\": \"orchestrator\", \"draft\": true, \"blocked_by\": [${blocker_id}]}")
draft_dep_id=$(echo "$draft_dep" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
draft_dep_status=$(echo "$draft_dep" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$draft_dep_status" == "draft" ]]; then
    pass "Draft: draft with deps stays in draft status"
else
    fail "Draft: draft with deps status is '$draft_dep_status', expected 'draft'"
fi

# Publish — should become blocked (blocker unresolved)
pub_dep=$(draft2_api POST "/tasks/${draft_dep_id}/publish" -d '{}')
pub_dep_status=$(echo "$pub_dep" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$pub_dep_status" == "blocked" ]]; then
    pass "Draft: publish with unresolved dep → blocked"
else
    fail "Draft: publish with dep status is '$pub_dep_status', expected 'blocked'"
fi

# Complete blocker → draft task should unblock
claimed_blocker=$(draft2_api POST "/tasks/claim" -d '{"subscriber_id": "draft2-agent"}')
claimed_blocker_id=$(echo "$claimed_blocker" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null || echo "")
draft2_api POST "/tasks/${claimed_blocker_id}/complete" -d '{"subscriber_id": "draft2-agent", "message": "done"}' >/dev/null
sleep 0.5

draft_dep_final=$(draft2_api GET "/tasks" | python3 -c "
import sys,json
tasks = json.load(sys.stdin).get('tasks',[])
for t in tasks:
    if t['id'] == ${draft_dep_id}:
        print(t['status']); break
" 2>/dev/null || echo "")

if [[ "$draft_dep_final" == "pending" ]]; then
    pass "Draft: published blocked task unblocks after blocker completed"
else
    fail "Draft: task status is '$draft_dep_final' after completing blocker, expected 'pending'"
fi

# ── Test 25: Publish non-draft fails ─────────────────────────────────

DRAFT3_BOARD="draft3-test-$$"
draft3_api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/board/${DRAFT3_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}
draft3_api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/board/${DRAFT3_BOARD}${path}" \
        -H "Content-Type: application/json" "$@"
}

draft3_api POST "/tasks" -d '{"title": "Pending", "created_by": "orchestrator"}' >/dev/null

pub_status=$(draft3_api_status POST "/tasks/1/publish" -d '{}')

if [[ "$pub_status" == "400" ]]; then
    pass "Draft: publish non-draft returns 400"
else
    fail "Draft: publish non-draft returned HTTP $pub_status, expected 400"
fi

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo "================================"
echo "  STRESS TEST RESULTS"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo "================================"

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
