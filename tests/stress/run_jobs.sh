#!/usr/bin/env bash
#
# Integration test for scheduled workflow jobs.
# Tests: create workflow, create scheduled job (job_type=workflow),
# list jobs, toggle, update, delete, and verify workflow linkage.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=8472
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
    rm -rf "$TMPDIR_JOBS" 2>/dev/null || true
}
trap cleanup EXIT

log()  { echo "[jobs-test] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }

api() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -X "$method" "${BASE_URL}${path}" \
        -H "Content-Type: application/json" "$@"
}

api_status() {
    local method="$1" path="$2"
    shift 2
    curl -s -m 10 -o /dev/null -w "%{http_code}" -X "$method" "${BASE_URL}${path}" \
        -H "Content-Type: application/json" "$@"
}

jq_py() {
    python3 -c "import sys,json; $1" 2>/dev/null || echo ""
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

TMPDIR_JOBS="$(mktemp -d)"
export CORAL_DATA_DIR="$TMPDIR_JOBS"

log "Building coral (dev mode)..."
cd "$CORAL_DIR"
go build -tags "dev fts5" -o "$TMPDIR_JOBS/coral" ./cmd/coral/

log "Starting coral server on port $PORT..."
"$TMPDIR_JOBS/coral" --host "$HOST" --port "$PORT" --backend tmux >"$TMPDIR_JOBS/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# ── Test 1: Create a workflow ───────────────────────────────────────

log "Test 1: Create a workflow..."
WF_RESULT=$(api POST "/api/workflows" -d '{
    "name": "test-scheduled-wf",
    "description": "Test workflow for scheduled jobs",
    "repo_path": "'"$TMPDIR_JOBS"'",
    "steps": [
        {"name": "echo-step", "type": "shell", "command": "echo hello"}
    ]
}')

WF_ID=$(echo "$WF_RESULT" | jq_py "print(json.load(sys.stdin).get('id',''))")
if [[ -n "$WF_ID" && "$WF_ID" != "None" ]]; then
    pass "Created workflow (id=$WF_ID)"
else
    fail "Failed to create workflow: $WF_RESULT"
fi

# ── Test 2: Create a prompt-type scheduled job ──────────────────────

log "Test 2: Create a prompt-type scheduled job..."
PROMPT_JOB=$(api POST "/api/scheduled/jobs" -d '{
    "name": "test-prompt-job",
    "cron_expr": "0 0 31 2 *",
    "repo_path": "'"$TMPDIR_JOBS"'",
    "prompt": "echo test"
}')

PROMPT_JOB_ID=$(echo "$PROMPT_JOB" | jq_py "print(json.load(sys.stdin).get('id',''))")
PROMPT_JOB_TYPE=$(echo "$PROMPT_JOB" | jq_py "print(json.load(sys.stdin).get('job_type',''))")

if [[ -n "$PROMPT_JOB_ID" && "$PROMPT_JOB_TYPE" == "prompt" ]]; then
    pass "Created prompt job (id=$PROMPT_JOB_ID, job_type=prompt)"
else
    fail "Failed to create prompt job: $PROMPT_JOB"
fi

# ── Test 3: Create a workflow-type scheduled job ────────────────────

log "Test 3: Create a workflow-type scheduled job..."
WF_JOB=$(api POST "/api/scheduled/jobs" -d '{
    "name": "test-workflow-job",
    "cron_expr": "0 0 31 2 *",
    "job_type": "workflow",
    "workflow_id": '"$WF_ID"'
}')

WF_JOB_ID=$(echo "$WF_JOB" | jq_py "print(json.load(sys.stdin).get('id',''))")
WF_JOB_TYPE=$(echo "$WF_JOB" | jq_py "print(json.load(sys.stdin).get('job_type',''))")
WF_JOB_WF_ID=$(echo "$WF_JOB" | jq_py "print(json.load(sys.stdin).get('workflow_id',''))")

if [[ "$WF_JOB_TYPE" == "workflow" && "$WF_JOB_WF_ID" == "$WF_ID" ]]; then
    pass "Created workflow job (id=$WF_JOB_ID, job_type=workflow, workflow_id=$WF_ID)"
else
    fail "Failed to create workflow job: $WF_JOB"
fi

# ── Test 4: Workflow job doesn't require prompt/repo_path ───────────

log "Test 4: Workflow job validation..."
STATUS_NO_WF=$(api_status POST "/api/scheduled/jobs" -d '{
    "name": "bad-wf-job",
    "cron_expr": "0 0 31 2 *",
    "job_type": "workflow"
}')

if [[ "$STATUS_NO_WF" == "400" ]]; then
    pass "Workflow job without workflow_id returns 400"
else
    fail "Expected 400 for missing workflow_id, got $STATUS_NO_WF"
fi

# ── Test 5: Prompt job requires prompt and repo_path ────────────────

log "Test 5: Prompt job validation..."
STATUS_NO_PROMPT=$(api_status POST "/api/scheduled/jobs" -d '{
    "name": "bad-prompt-job",
    "cron_expr": "0 0 31 2 *",
    "repo_path": "'"$TMPDIR_JOBS"'"
}')

if [[ "$STATUS_NO_PROMPT" == "400" ]]; then
    pass "Prompt job without prompt returns 400"
else
    fail "Expected 400 for missing prompt, got $STATUS_NO_PROMPT"
fi

# ── Test 6: List jobs shows both types ──────────────────────────────

log "Test 6: List jobs..."
JOBS_LIST=$(api GET "/api/scheduled/jobs")
JOB_COUNT=$(echo "$JOBS_LIST" | jq_py "print(len(json.load(sys.stdin).get('jobs', [])))")

if [[ "$JOB_COUNT" -ge 2 ]]; then
    pass "List jobs returns at least 2 jobs (got $JOB_COUNT)"
else
    fail "Expected at least 2 jobs, got $JOB_COUNT"
fi

# ── Test 7: Get individual job ──────────────────────────────────────

log "Test 7: Get workflow job..."
GOT_JOB=$(api GET "/api/scheduled/jobs/$WF_JOB_ID")
GOT_TYPE=$(echo "$GOT_JOB" | jq_py "print(json.load(sys.stdin).get('job_type',''))")

if [[ "$GOT_TYPE" == "workflow" ]]; then
    pass "Get job returns correct job_type=workflow"
else
    fail "Expected job_type=workflow, got $GOT_TYPE"
fi

# ── Test 8: Update job ──────────────────────────────────────────────

log "Test 8: Update workflow job description..."
UPDATED=$(api PUT "/api/scheduled/jobs/$WF_JOB_ID" -d '{"description": "updated desc"}')
UPDATED_DESC=$(echo "$UPDATED" | jq_py "print(json.load(sys.stdin).get('description',''))")

if [[ "$UPDATED_DESC" == "updated desc" ]]; then
    pass "Update job description works"
else
    fail "Expected 'updated desc', got '$UPDATED_DESC'"
fi

# ── Test 9: Toggle job ──────────────────────────────────────────────

log "Test 9: Toggle job enabled..."
TOGGLED=$(api POST "/api/scheduled/jobs/$WF_JOB_ID/toggle")
TOGGLED_ENABLED=$(echo "$TOGGLED" | jq_py "print(json.load(sys.stdin).get('enabled',''))")

if [[ "$TOGGLED_ENABLED" == "0" ]]; then
    pass "Toggle disabled the job"
else
    fail "Expected enabled=0, got $TOGGLED_ENABLED"
fi

# Toggle back
api POST "/api/scheduled/jobs/$WF_JOB_ID/toggle" >/dev/null

# ── Test 10: Delete jobs ────────────────────────────────────────────

log "Test 10: Delete jobs..."
DEL_STATUS=$(api_status DELETE "/api/scheduled/jobs/$PROMPT_JOB_ID")
if [[ "$DEL_STATUS" == "200" ]]; then
    pass "Deleted prompt job"
else
    fail "Delete prompt job returned $DEL_STATUS"
fi

DEL_STATUS=$(api_status DELETE "/api/scheduled/jobs/$WF_JOB_ID")
if [[ "$DEL_STATUS" == "200" ]]; then
    pass "Deleted workflow job"
else
    fail "Delete workflow job returned $DEL_STATUS"
fi

# ── Test 11: Workflow CRUD still works after job deletion ───────────

log "Test 11: Workflow still exists after job deletion..."
WF_CHECK=$(api GET "/api/workflows/$WF_ID")
WF_CHECK_NAME=$(echo "$WF_CHECK" | jq_py "print(json.load(sys.stdin).get('name',''))")

if [[ "$WF_CHECK_NAME" == "test-scheduled-wf" ]]; then
    pass "Workflow not cascaded by job deletion"
else
    fail "Workflow missing after job deletion"
fi

# Clean up workflow
api DELETE "/api/workflows/$WF_ID" >/dev/null

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo "================================"
echo "  JOBS TEST RESULTS"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo "================================"

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
