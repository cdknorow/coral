#!/usr/bin/env bash
#
# Integration test for the Coral workflow system.
# Tests workflow CRUD, triggering, execution, run status, step outputs,
# validation, and error handling via the HTTP API.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=$((8500 + RANDOM % 500))
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
    rm -rf "$TMPDIR_WF" 2>/dev/null || true
}
trap cleanup EXIT

log()  { echo "[workflow-test] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }

api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}/api/workflows${path}" \
        -H "Content-Type: application/json" "$@"
}

api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}/api/workflows${path}" \
        -H "Content-Type: application/json" "$@"
}

# jq_val evaluates a Python expression with 'd' as the parsed JSON.
# Usage: echo '{"a":1}' | jq_val ".get('a','')"     → 1
#        echo '{"a":[1,2]}' | jq_py "len(d.get('a',[]))" → 2
jq_val() {
    python3 -c "
import sys, json, signal
signal.alarm(5)
try:
    d = json.load(sys.stdin)
    print(d$1)
except:
    print('')
" 2>/dev/null || echo ""
}

# jq_py evaluates an arbitrary Python expression with 'd' as the parsed JSON.
jq_py() {
    python3 -c "
import sys, json, signal
signal.alarm(5)
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1]))
except:
    print('')
" "$1" 2>/dev/null || echo ""
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

# Wait for a workflow run to reach a terminal state
wait_for_run() {
    local run_id="$1"
    local timeout="${2:-15}"
    local elapsed=0
    while [[ $elapsed -lt $timeout ]]; do
        local status
        status=$(api GET "/runs/${run_id}" | jq_val ".get('status','')")
        case "$status" in
            completed|failed|killed) echo "$status"; return 0 ;;
        esac
        sleep 0.5
        elapsed=$((elapsed + 1))
    done
    echo "timeout"
    return 1
}

# ── Setup ────────────────────────────────────────────────────────────

TMPDIR_WF="$(mktemp -d)"
export CORAL_DATA_DIR="$TMPDIR_WF"

# Create a repo directory for workflows to use
REPO_PATH="$TMPDIR_WF/test-repo"
mkdir -p "$REPO_PATH"

log "Building coral (dev mode)..."
cd "$CORAL_DIR"
go build -tags "dev fts5" -o "$TMPDIR_WF/coral" ./cmd/coral/

log "Starting coral server on port $PORT..."
"$TMPDIR_WF/coral" --host "$HOST" --port "$PORT" --backend tmux >"$TMPDIR_WF/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# ═══════════════════════════════════════════════════════════════════
# Test 1: Create a workflow
# ═══════════════════════════════════════════════════════════════════

log "Test 1: Create a workflow..."
result=$(api POST "" -d "{
    \"name\": \"test-echo\",
    \"description\": \"A simple echo workflow\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"step1\", \"type\": \"shell\", \"command\": \"echo hello-world\"}
    ]
}")
WF_ID=$(echo "$result" | jq_val ".get('id','')")
WF_NAME=$(echo "$result" | jq_val ".get('name','')")

if [[ -n "$WF_ID" && "$WF_NAME" == "test-echo" ]]; then
    pass "Created workflow id=$WF_ID name=$WF_NAME"
else
    fail "Create workflow returned unexpected result: $result"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 2: Get workflow by ID and by name
# ═══════════════════════════════════════════════════════════════════

log "Test 2: Get workflow by ID and by name..."
get_id_name=$(api GET "/${WF_ID}" | jq_val ".get('name','')")
get_name_name=$(api GET "/by-name/test-echo" | jq_val ".get('name','')")

if [[ "$get_id_name" == "test-echo" && "$get_name_name" == "test-echo" ]]; then
    pass "Get by ID and by name both return correct workflow"
else
    fail "Get by ID returned '$get_id_name', by name returned '$get_name_name'"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 3: List workflows
# ═══════════════════════════════════════════════════════════════════

log "Test 3: List workflows..."
list_count=$(api GET "" | jq_py "len(d.get('workflows', []))")
if [[ "$list_count" == "2" ]]; then
    pass "List returns 2 workflows (1 seeded demo + 1 test)"
else
    fail "List returned $list_count workflows, expected 2"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 4: Trigger workflow and verify execution
# ═══════════════════════════════════════════════════════════════════

log "Test 4: Trigger workflow..."
trigger_result=$(api POST "/${WF_ID}/trigger" -d '{}')
RUN_ID=$(echo "$trigger_result" | jq_val ".get('run_id','')")
trigger_status=$(echo "$trigger_result" | jq_val ".get('status','')")

if [[ -n "$RUN_ID" ]]; then
    pass "Triggered workflow, run_id=$RUN_ID status=$trigger_status"
else
    fail "Trigger returned unexpected result: $trigger_result"
fi

# Wait for run to complete
run_final=$(wait_for_run "$RUN_ID" 15)
if [[ "$run_final" == "completed" ]]; then
    pass "Run completed successfully"
else
    fail "Run ended with status '$run_final', expected 'completed'"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 5: Verify run details and step results
# ═══════════════════════════════════════════════════════════════════

log "Test 5: Verify run details..."
run_detail=$(api GET "/runs/${RUN_ID}")
run_status=$(echo "$run_detail" | jq_val ".get('status','')")
step_count=$(echo "$run_detail" | jq_py "len(d.get('steps', []) or [])")

if [[ "$run_status" == "completed" ]]; then
    pass "Run status is 'completed'"
else
    fail "Run status is '$run_status', expected 'completed'"
fi

if [[ "$step_count" == "1" ]]; then
    pass "Run has 1 step result"
else
    fail "Run has $step_count step results, expected 1"
fi

# Check step output contains our echo
step_output=$(echo "$run_detail" | jq_py "(d.get('steps', []) or [{}])[0].get('output_tail','')")
if echo "$step_output" | grep -q "hello-world"; then
    pass "Step output contains 'hello-world'"
else
    fail "Step output missing 'hello-world': $step_output"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 6: Trigger by name
# ═══════════════════════════════════════════════════════════════════

log "Test 6: Trigger by name..."
trigger_name_result=$(api POST "/by-name/test-echo/trigger" -d '{}')
RUN_ID2=$(echo "$trigger_name_result" | jq_val ".get('run_id','')")

if [[ -n "$RUN_ID2" ]]; then
    run_final2=$(wait_for_run "$RUN_ID2" 15)
    if [[ "$run_final2" == "completed" ]]; then
        pass "Trigger by name: run completed"
    else
        fail "Trigger by name: run status '$run_final2'"
    fi
else
    fail "Trigger by name failed: $trigger_name_result"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 7: List runs for workflow
# ═══════════════════════════════════════════════════════════════════

log "Test 7: List runs..."
runs_count=$(api GET "/${WF_ID}/runs" | jq_py "len(d.get('runs', []))")
if [[ "$runs_count" == "2" ]]; then
    pass "Workflow has 2 runs"
else
    fail "Workflow has $runs_count runs, expected 2"
fi

# Recent runs across all workflows
recent_count=$(api GET "/runs/recent" | jq_py "len(d.get('runs', []))")
if [[ "$recent_count" == "2" ]]; then
    pass "Recent runs returns 2"
else
    fail "Recent runs returned $recent_count, expected 2"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 8: Multi-step workflow with cross-step data
# ═══════════════════════════════════════════════════════════════════

log "Test 8: Multi-step workflow..."
multi_result=$(api POST "" -d "{
    \"name\": \"test-multi\",
    \"description\": \"Multi-step workflow\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"produce\", \"type\": \"shell\", \"command\": \"echo PIPELINE_DATA_42\"},
        {\"name\": \"consume\", \"type\": \"shell\", \"command\": \"cat \$CORAL_PREV_STDOUT\"}
    ]
}")
MULTI_ID=$(echo "$multi_result" | jq_val ".get('id','')")

if [[ -n "$MULTI_ID" ]]; then
    multi_trigger=$(api POST "/${MULTI_ID}/trigger" -d '{}')
    MULTI_RUN=$(echo "$multi_trigger" | jq_val ".get('run_id','')")
    multi_final=$(wait_for_run "$MULTI_RUN" 15)

    if [[ "$multi_final" == "completed" ]]; then
        # Verify step 2 consumed step 1's output
        run_detail=$(api GET "/runs/${MULTI_RUN}")
        step2_output=$(echo "$run_detail" | jq_py "(d.get('steps', []) or [{}]*2)[1].get('output_tail','')")
        if echo "$step2_output" | grep -q "PIPELINE_DATA_42"; then
            pass "Multi-step: step 2 received step 1 output via \$CORAL_PREV_STDOUT"
        else
            fail "Multi-step: step 2 output missing PIPELINE_DATA_42: $step2_output"
        fi
    else
        fail "Multi-step run status '$multi_final', expected 'completed'"
    fi
else
    fail "Failed to create multi-step workflow"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 9: Step failure handling
# ═══════════════════════════════════════════════════════════════════

log "Test 9: Step failure (no continue_on_failure)..."
fail_result=$(api POST "" -d "{
    \"name\": \"test-fail\",
    \"description\": \"Failing workflow\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"bad-step\", \"type\": \"shell\", \"command\": \"exit 1\"},
        {\"name\": \"should-skip\", \"type\": \"shell\", \"command\": \"echo never\"}
    ]
}")
FAIL_ID=$(echo "$fail_result" | jq_val ".get('id','')")

fail_trigger=$(api POST "/${FAIL_ID}/trigger" -d '{}')
FAIL_RUN=$(echo "$fail_trigger" | jq_val ".get('run_id','')")
fail_final=$(wait_for_run "$FAIL_RUN" 15)

if [[ "$fail_final" == "failed" ]]; then
    pass "Failing workflow: run status is 'failed'"
else
    fail "Failing workflow: run status '$fail_final', expected 'failed'"
fi

# Verify step 2 was skipped
run_detail=$(api GET "/runs/${FAIL_RUN}")
step2_status=$(echo "$run_detail" | jq_py "(d.get('steps', []) or [{}]*2)[1].get('status','')")
if [[ "$step2_status" == "skipped" ]]; then
    pass "Failing workflow: step 2 was skipped"
else
    fail "Failing workflow: step 2 status '$step2_status', expected 'skipped'"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 10: continue_on_failure
# ═══════════════════════════════════════════════════════════════════

log "Test 10: continue_on_failure..."
cof_result=$(api POST "" -d "{
    \"name\": \"test-continue\",
    \"description\": \"Continue on failure\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"fail-ok\", \"type\": \"shell\", \"command\": \"exit 1\", \"continue_on_failure\": true},
        {\"name\": \"runs-anyway\", \"type\": \"shell\", \"command\": \"echo continued\"}
    ]
}")
COF_ID=$(echo "$cof_result" | jq_val ".get('id','')")

cof_trigger=$(api POST "/${COF_ID}/trigger" -d '{}')
COF_RUN=$(echo "$cof_trigger" | jq_val ".get('run_id','')")
cof_final=$(wait_for_run "$COF_RUN" 15)

if [[ "$cof_final" == "completed" ]]; then
    pass "continue_on_failure: run completed despite step 1 failure"
else
    fail "continue_on_failure: run status '$cof_final', expected 'completed'"
fi

# Verify step 2 ran
run_detail=$(api GET "/runs/${COF_RUN}")
step2_output=$(echo "$run_detail" | jq_py "(d.get('steps', []) or [{}]*2)[1].get('output_tail','')")
if echo "$step2_output" | grep -q "continued"; then
    pass "continue_on_failure: step 2 executed"
else
    fail "continue_on_failure: step 2 output missing 'continued': $step2_output"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 11: Update workflow
# ═══════════════════════════════════════════════════════════════════

log "Test 11: Update workflow..."
update_status=$(api_status PUT "/${WF_ID}" -d '{"description": "Updated description"}')
if [[ "$update_status" == "200" ]]; then
    updated_desc=$(api GET "/${WF_ID}" | jq_val ".get('description','')")
    if [[ "$updated_desc" == "Updated description" ]]; then
        pass "Update workflow description"
    else
        fail "Update: description is '$updated_desc'"
    fi
else
    fail "Update returned HTTP $update_status"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 12: Disable workflow prevents triggering
# ═══════════════════════════════════════════════════════════════════

log "Test 12: Disabled workflow rejects trigger..."
api PUT "/${WF_ID}" -d '{"enabled": 0}' >/dev/null
disabled_status=$(api_status POST "/${WF_ID}/trigger" -d '{}')
api PUT "/${WF_ID}" -d '{"enabled": 1}' >/dev/null  # re-enable

if [[ "$disabled_status" == "409" ]]; then
    pass "Disabled workflow returns 409 on trigger"
else
    fail "Disabled workflow trigger returned HTTP $disabled_status, expected 409"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 13: Validation — empty name
# ═══════════════════════════════════════════════════════════════════

log "Test 13: Validation tests..."
v_status=$(api_status POST "" -d "{
    \"name\": \"\",
    \"steps\": [{\"name\": \"s\", \"type\": \"shell\", \"command\": \"echo x\"}]
}")
if [[ "$v_status" == "400" ]]; then
    pass "Validation: empty name returns 400"
else
    fail "Validation: empty name returned HTTP $v_status"
fi

# Invalid name characters
v_status=$(api_status POST "" -d "{
    \"name\": \"has spaces\",
    \"steps\": [{\"name\": \"s\", \"type\": \"shell\", \"command\": \"echo x\"}]
}")
if [[ "$v_status" == "400" ]]; then
    pass "Validation: invalid name chars returns 400"
else
    fail "Validation: invalid name chars returned HTTP $v_status"
fi

# No steps
v_status=$(api_status POST "" -d "{\"name\": \"empty-steps\", \"steps\": []}")
if [[ "$v_status" == "400" ]]; then
    pass "Validation: empty steps returns 400"
else
    fail "Validation: empty steps returned HTTP $v_status"
fi

# Duplicate name
v_status=$(api_status POST "" -d "{
    \"name\": \"test-echo\",
    \"steps\": [{\"name\": \"s\", \"type\": \"shell\", \"command\": \"echo x\"}]
}")
if [[ "$v_status" == "409" ]]; then
    pass "Validation: duplicate name returns 409"
else
    fail "Validation: duplicate name returned HTTP $v_status"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 14: Delete workflow (cascades to runs)
# ═══════════════════════════════════════════════════════════════════

log "Test 14: Delete workflow..."
del_status=$(api_status DELETE "/${WF_ID}")
if [[ "$del_status" == "200" ]]; then
    pass "Delete workflow returns 200"
else
    fail "Delete returned HTTP $del_status"
fi

# Verify gone
get_status=$(api_status GET "/${WF_ID}")
if [[ "$get_status" == "404" ]]; then
    pass "Deleted workflow returns 404"
else
    fail "Deleted workflow returned HTTP $get_status, expected 404"
fi

# Verify runs were cascade deleted
run_status=$(api_status GET "/runs/${RUN_ID}")
if [[ "$run_status" == "404" ]]; then
    pass "Cascade: run deleted with workflow"
else
    fail "Cascade: run returned HTTP $run_status, expected 404"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 15: Invalid workflow ID returns 400
# ═══════════════════════════════════════════════════════════════════

log "Test 15: Invalid IDs..."
bad_id_status=$(api_status GET "/abc")
if [[ "$bad_id_status" == "400" ]]; then
    pass "Non-numeric workflow ID returns 400"
else
    fail "Non-numeric workflow ID returned HTTP $bad_id_status"
fi

bad_run_status=$(api_status GET "/runs/abc")
if [[ "$bad_run_status" == "400" ]]; then
    pass "Non-numeric run ID returns 400"
else
    fail "Non-numeric run ID returned HTTP $bad_run_status"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 16: Kill running workflow
# ═══════════════════════════════════════════════════════════════════

log "Test 16: Kill running workflow..."
kill_result=$(api POST "" -d "{
    \"name\": \"test-kill\",
    \"description\": \"Long-running workflow for kill test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"long-step\", \"type\": \"shell\", \"command\": \"sleep 60\"}
    ]
}")
KILL_WF_ID=$(echo "$kill_result" | jq_val ".get('id','')")
kill_trigger=$(api POST "/${KILL_WF_ID}/trigger" -d '{}')
KILL_RUN_ID=$(echo "$kill_trigger" | jq_val ".get('run_id','')")

# Wait a moment for it to start running
sleep 1

# Kill it
kill_status=$(api_status POST "/runs/${KILL_RUN_ID}/kill")
if [[ "$kill_status" == "200" ]]; then
    pass "Kill returns 200"
else
    fail "Kill returned HTTP $kill_status"
fi

# Wait for killed status
kill_final=$(wait_for_run "$KILL_RUN_ID" 10)
if [[ "$kill_final" == "killed" ]]; then
    pass "Killed workflow: run status is 'killed'"
else
    fail "Killed workflow: run status '$kill_final', expected 'killed'"
fi

# Kill already-killed run should return 409
kill_again_status=$(api_status POST "/runs/${KILL_RUN_ID}/kill")
if [[ "$kill_again_status" == "409" ]]; then
    pass "Kill already-killed run returns 409"
else
    fail "Kill already-killed returned HTTP $kill_again_status, expected 409"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 17: Trigger with context
# ═══════════════════════════════════════════════════════════════════

log "Test 17: Trigger with context..."
ctx_result=$(api POST "" -d "{
    \"name\": \"test-context\",
    \"description\": \"Context test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"check-ctx\", \"type\": \"shell\", \"command\": \"echo ctx-ok\"}
    ]
}")
CTX_WF_ID=$(echo "$ctx_result" | jq_val ".get('id','')")
ctx_trigger=$(api POST "/${CTX_WF_ID}/trigger" -d '{"trigger_type": "webhook", "context": {"source": "github", "pr": 42}}')
CTX_RUN_ID=$(echo "$ctx_trigger" | jq_val ".get('run_id','')")
ctx_trigger_type=$(echo "$ctx_trigger" | jq_val ".get('trigger_type','')")

if [[ "$ctx_trigger_type" == "webhook" ]]; then
    pass "Trigger with custom trigger_type"
else
    fail "Trigger type is '$ctx_trigger_type', expected 'webhook'"
fi

wait_for_run "$CTX_RUN_ID" 15 >/dev/null

# ═══════════════════════════════════════════════════════════════════
# Test 18: Nonexistent workflow returns 404
# ═══════════════════════════════════════════════════════════════════

log "Test 18: 404 for nonexistent resources..."
ne_status=$(api_status GET "/99999")
if [[ "$ne_status" == "404" ]]; then
    pass "Nonexistent workflow returns 404"
else
    fail "Nonexistent workflow returned HTTP $ne_status"
fi

ne_run_status=$(api_status GET "/runs/99999")
if [[ "$ne_run_status" == "404" ]]; then
    pass "Nonexistent run returns 404"
else
    fail "Nonexistent run returned HTTP $ne_run_status"
fi

ne_trigger_status=$(api_status POST "/99999/trigger" -d '{}')
if [[ "$ne_trigger_status" == "404" ]]; then
    pass "Trigger nonexistent workflow returns 404"
else
    fail "Trigger nonexistent returned HTTP $ne_trigger_status"
fi

ne_name_status=$(api_status GET "/by-name/nonexistent")
if [[ "$ne_name_status" == "404" ]]; then
    pass "Get by nonexistent name returns 404"
else
    fail "Get by nonexistent name returned HTTP $ne_name_status"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 19: Environment variables in steps
# ═══════════════════════════════════════════════════════════════════

log "Test 19: Workflow environment variables..."
env_result=$(api POST "" -d "{
    \"name\": \"test-env\",
    \"description\": \"Env var test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {\"name\": \"check-env\", \"type\": \"shell\", \"command\": \"echo NAME=\$CORAL_WORKFLOW_NAME STEP=\$CORAL_WORKFLOW_STEP\"}
    ]
}")
ENV_WF_ID=$(echo "$env_result" | jq_val ".get('id','')")
env_trigger=$(api POST "/${ENV_WF_ID}/trigger" -d '{}')
ENV_RUN_ID=$(echo "$env_trigger" | jq_val ".get('run_id','')")
wait_for_run "$ENV_RUN_ID" 15 >/dev/null

run_detail=$(api GET "/runs/${ENV_RUN_ID}")
env_output=$(echo "$run_detail" | jq_py "(d.get('steps', []) or [{}])[0].get('output_tail','')")
if echo "$env_output" | grep -q "NAME=test-env" && echo "$env_output" | grep -q "STEP=0"; then
    pass "Workflow env vars CORAL_WORKFLOW_NAME and CORAL_WORKFLOW_STEP are set"
else
    fail "Env var output: $env_output"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 20: Runs list with status filter
# ═══════════════════════════════════════════════════════════════════

log "Test 20: Run status filter..."
# We have completed and failed and killed runs at this point
completed_runs=$(api GET "/runs/recent?status=completed" | jq_py "len(d.get('runs', []))")
failed_runs=$(api GET "/runs/recent?status=failed" | jq_py "len(d.get('runs', []))")

if [[ "$completed_runs" -ge 1 ]]; then
    pass "Status filter: found $completed_runs completed runs"
else
    fail "Status filter: found $completed_runs completed runs, expected >=1"
fi

if [[ "$failed_runs" -ge 1 ]]; then
    pass "Status filter: found $failed_runs failed runs"
else
    fail "Status filter: found $failed_runs failed runs, expected >=1"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 21: StepComplete hook fires on success
# ═══════════════════════════════════════════════════════════════════

log "Test 21: StepComplete hook fires on success..."
HOOK_MARKER="$TMPDIR_WF/hook_marker_complete"
hook_result=$(api POST "" -d "{
    \"name\": \"test-hook-complete\",
    \"description\": \"Hook test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {
            \"name\": \"hooked-step\",
            \"type\": \"shell\",
            \"command\": \"echo hook-test\",
            \"hooks\": {
                \"StepComplete\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"touch $HOOK_MARKER\"}]}]
            }
        }
    ]
}")
HOOK_WF_ID=$(echo "$hook_result" | jq_val ".get('id','')")

hook_trigger=$(api POST "/${HOOK_WF_ID}/trigger" -d '{}')
HOOK_RUN_ID=$(echo "$hook_trigger" | jq_val ".get('run_id','')")
hook_final=$(wait_for_run "$HOOK_RUN_ID" 15)

if [[ "$hook_final" == "completed" ]]; then
    pass "Hook workflow completed"
else
    fail "Hook workflow status '$hook_final', expected 'completed'"
fi

# Give hook a moment to execute (it's best-effort/async)
sleep 1

if [[ -f "$HOOK_MARKER" ]]; then
    pass "StepComplete hook fired: marker file exists"
else
    fail "StepComplete hook did not fire: marker file missing"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 22: StepFailed hook fires on failure
# ═══════════════════════════════════════════════════════════════════

log "Test 22: StepFailed hook fires on failure..."
FAIL_HOOK_MARKER="$TMPDIR_WF/hook_marker_failed"
fail_hook_result=$(api POST "" -d "{
    \"name\": \"test-hook-failed\",
    \"description\": \"Failed hook test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {
            \"name\": \"fail-hooked\",
            \"type\": \"shell\",
            \"command\": \"exit 1\",
            \"hooks\": {
                \"StepFailed\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"touch $FAIL_HOOK_MARKER\"}]}]
            }
        }
    ]
}")
FAIL_HOOK_WF_ID=$(echo "$fail_hook_result" | jq_val ".get('id','')")

fail_hook_trigger=$(api POST "/${FAIL_HOOK_WF_ID}/trigger" -d '{}')
FAIL_HOOK_RUN_ID=$(echo "$fail_hook_trigger" | jq_val ".get('run_id','')")
fail_hook_final=$(wait_for_run "$FAIL_HOOK_RUN_ID" 15)

if [[ "$fail_hook_final" == "failed" ]]; then
    pass "Failed hook workflow has status 'failed'"
else
    fail "Failed hook workflow status '$fail_hook_final', expected 'failed'"
fi

sleep 1

if [[ -f "$FAIL_HOOK_MARKER" ]]; then
    pass "StepFailed hook fired: marker file exists"
else
    fail "StepFailed hook did not fire: marker file missing"
fi

# ═══════════════════════════════════════════════════════════════════
# Test 23: Hook does not fire for wrong event
# ═══════════════════════════════════════════════════════════════════

log "Test 23: Hook does not fire for wrong event..."
WRONG_HOOK_MARKER="$TMPDIR_WF/hook_marker_wrong"
wrong_hook_result=$(api POST "" -d "{
    \"name\": \"test-hook-wrong\",
    \"description\": \"Wrong event hook test\",
    \"repo_path\": \"$REPO_PATH\",
    \"steps\": [
        {
            \"name\": \"ok-step\",
            \"type\": \"shell\",
            \"command\": \"echo success\",
            \"hooks\": {
                \"StepFailed\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"touch $WRONG_HOOK_MARKER\"}]}]
            }
        }
    ]
}")
WRONG_HOOK_WF_ID=$(echo "$wrong_hook_result" | jq_val ".get('id','')")

wrong_hook_trigger=$(api POST "/${WRONG_HOOK_WF_ID}/trigger" -d '{}')
WRONG_HOOK_RUN_ID=$(echo "$wrong_hook_trigger" | jq_val ".get('run_id','')")
wait_for_run "$WRONG_HOOK_RUN_ID" 15 >/dev/null

sleep 1

if [[ ! -f "$WRONG_HOOK_MARKER" ]]; then
    pass "StepFailed hook did NOT fire on successful step (correct)"
else
    fail "StepFailed hook fired on a successful step (should not have)"
fi

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo "================================"
echo "  WORKFLOW TEST RESULTS"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo "================================"

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
