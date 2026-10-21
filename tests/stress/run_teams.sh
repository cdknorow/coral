#!/usr/bin/env bash
#
# Integration test for team lifecycle with git worktrees.
# Tests worktree lifecycle and board membership across agent restarts.
#
set -euo pipefail
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=${CORAL_TEST_PORT:-8473}
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
PASS=0
FAIL=0
SERVER_PID=""

# ── Helpers ──────────────────────────────────────────────────────────

cleanup() {
    local exit_code=$?
    coral_test_watchdog_stop
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    # The tmux server on this test's isolated socket outlives the coral server, and its panes are
    # the mock-agents (they run as its panes); killing it ends them too, so the run leaves nothing
    # behind (#3812). No kill-by-pattern (Security's SHOULD); the var is guarded because the trap is
    # armed before it is set.
    if [[ -n "${CORAL_DATA_DIR:-}" && -S "$CORAL_DATA_DIR/tmux.sock" ]]; then
        tmux -S "$CORAL_DATA_DIR/tmux.sock" kill-server 2>/dev/null || true
    fi
    coral_test_sandbox_cleanup   # #4354: its check first (a crash path reads the stub marker), then the folders
    if [[ $exit_code -ne 0 || $FAIL -gt 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log: $TMPDIR_TEAMS"
    else
        coral_test_rm_tree "$TMPDIR_TEAMS"
    fi
}
trap cleanup EXIT
coral_test_watchdog   # a hang fails this runner within CORAL_TEST_WATCHDOG_S (default 900 s), not the whole run (#116)

log()  { echo "[teams-test] $*"; }
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

TMPDIR_TEAMS="$(mktemp -d)"
export TMPDIR="$TMPDIR_TEAMS"  # (#3926)
if lsof -iTCP:"$PORT" -sTCP:LISTEN -t >/dev/null 2>&1; then
    log "ERROR: Port $PORT is already in use; set CORAL_TEST_PORT to a free port."
    exit 1
fi
export CORAL_DATA_DIR="$TMPDIR_TEAMS/coral-data"
export CORAL_NO_DEMO_SEED=1  # no demo workflow/job (it would run a real agent)
export CORAL_NO_WELCOME=1    # no first-launch screen (#3221)
export CORAL_TMUX_NO_FALLBACK=1
mkdir -p "$CORAL_DATA_DIR"

log "Building coral (dev mode) and mock-agent..."
cd "$CORAL_DIR"
# This build's coral-board and coral-hook-* next to coral: a dev or test server never uses the
# installed app's (#3492), so without them its agents would send no status events.
go build -tags "dev fts5" -o "$TMPDIR_TEAMS/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync
go build -o "$TMPDIR_TEAMS/mock-agent${CORAL_TEST_EXE}" ./cmd/mock-agent/
# The mock as the server reads it: a native path, with .exe on Windows (a Windows server finds a CLI only by its extension).
MOCK_AGENT_NATIVE="$(coral_test_native_path "$TMPDIR_TEAMS/mock-agent${CORAL_TEST_EXE}")"

# Create a temp git repo with an initial commit
REPO_DIR="$TMPDIR_TEAMS/test-repo"
mkdir -p "$REPO_DIR"
git -C "$REPO_DIR" init -b main >/dev/null 2>&1
echo "hello" > "$REPO_DIR/README.md"
git -C "$REPO_DIR" add . >/dev/null 2>&1
git -C "$REPO_DIR" -c user.name="Test" -c user.email="test@test.com" commit -m "initial" >/dev/null 2>&1

log "Starting coral server on port $PORT..."
# The server gets its own HOME and no host-data dirs, so it never indexes this machine's real
# ~/.claude/projects, ~/.codex, ~/.gemini or ~/.pi (privacy and deterministic results).
mkdir -p "$TMPDIR_TEAMS/host-home"
coral_test_sandbox   # #4354: stub claude/codex/gemini first on PATH; checked at the end
env -u CORAL_PORT -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR HOME="$TMPDIR_TEAMS/host-home" USERPROFILE="$TMPDIR_TEAMS/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" "$TMPDIR_TEAMS/coral" --home "$CORAL_DATA_DIR" --no-browser --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMPDIR_TEAMS/server.log" 2>&1 &
SERVER_PID=$!
wait_for_server

# Set CLI path to mock-agent so we don't need the real claude binary
api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}" >/dev/null

TEAM_NAME="wt-test-$$"
# WORKTREE_PATH and WORKTREE_BRANCH are discovered from the launch result below: the product
# names the worktree worktrees/b-<hash> (branch coral-team/b-<hash>), not after the team (#3812).
WORKTREE_PATH=""
WORKTREE_BRANCH=""

# ── Test 1: Launch team with worktree ────────────────────────────────

log "Test 1: Launch team with worktree..."
launch_result=$(api POST "/api/sessions/launch-team" -d "{
    \"board_name\": \"$TEAM_NAME\",
    \"working_dir\": \"$(coral_test_native_path "$REPO_DIR")\",
    \"worktree\": true,
    \"base_branch\": \"main\",
    \"agents\": [
        {\"agent_type\": \"claude\", \"name\": \"Dev Agent\", \"prompt\": \"You are a developer.\"},
        {\"agent_type\": \"claude\", \"name\": \"QA Agent\", \"prompt\": \"You are a tester.\"}
    ]
}")

launch_ok=$(echo "$launch_result" | python3 -c "import sys,json; d=json.load(sys.stdin); print('yes' if d.get('ok') or d.get('sessions') else 'no')" 2>/dev/null || echo "no")

# A launch starts a board with its own id (#3536: b-...); the team is stored, and found, by it.
TEAM_BOARD_ID=$(echo "$launch_result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('board_id',''))" 2>/dev/null || echo "")
if [[ "$launch_ok" == "yes" ]]; then
    pass "Team launched successfully"
else
    fail "Team launch failed: $launch_result"
fi

# Wait for agents to start
sleep 3

# Discover the real worktree from the launch result (the product names it worktrees/b-<hash>).
WORKTREE_PATH=$(echo "$launch_result" | python3 -c "import sys,json; d=json.load(sys.stdin); print(next((a.get('worktree_path','') for a in d.get('agents',[]) if a.get('worktree_path')), ''))" 2>/dev/null)
WORKTREE_BRANCH="coral-team/$(basename "$WORKTREE_PATH")"

# ── Test 2: Worktree directory created ───────────────────────────────

log "Test 2: Verify worktree directory exists..."
if [[ -d "$WORKTREE_PATH" ]]; then
    pass "Worktree directory created at $WORKTREE_PATH"
else
    fail "Worktree directory not found at $WORKTREE_PATH"
fi

# ── Test 3: Worktree is on correct branch ────────────────────────────

log "Test 3: Verify worktree branch..."
if [[ -d "$WORKTREE_PATH" ]]; then
    wt_branch=$(git -C "$WORKTREE_PATH" branch --show-current 2>/dev/null || echo "")
    if [[ "$wt_branch" == "$WORKTREE_BRANCH" ]]; then
        pass "Worktree is on branch $WORKTREE_BRANCH"
    else
        fail "Worktree branch is '$wt_branch', expected '$WORKTREE_BRANCH'"
    fi
else
    fail "Cannot check branch — worktree directory missing"
fi

# ── Test 4: git worktree list shows the worktree ─────────────────────

log "Test 4: Verify git worktree list..."
wt_listed=$(git -C "$REPO_DIR" worktree list 2>/dev/null | grep "coral-team/" | wc -l | tr -d ' ')
if [[ "$wt_listed" -ge 1 ]]; then
    pass "git worktree list includes the team worktree"
else
    fail "git worktree list does not show the worktree"
fi

# ── Test 5: Agents launched into worktree directory ──────────────────

log "Test 5: Verify agents launched with worktree path..."
agents_with_worktree=$(echo "$launch_result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
agents = data.get('agents', [])
count = sum(1 for a in agents if (a.get('worktree_path') or ''))
print(count)
" 2>/dev/null || echo "0")

if [[ "$agents_with_worktree" -ge 2 ]]; then
    pass "Both agents launched with worktree path"
else
    fail "Expected 2 agents with worktree path, found $agents_with_worktree"
fi

# ── Test 6: Team appears in teams API ────────────────────────────────

log "Test 6: Verify team in teams API..."
team_status=$(api GET "/api/teams/detail/${TEAM_BOARD_ID:-$TEAM_NAME}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

if [[ "$team_status" == "running" ]]; then
    pass "Team appears in API with status 'running'"
else
    fail "Team status is '$team_status', expected 'running'"
fi

# ── Test 7: Kill team ────────────────────────────────────────────────

log "Test 7: Kill team..."
# Get session names and IDs from launch result
agent_info=$(echo "$launch_result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    print(a.get('session_name', '') + ' ' + a.get('session_id', ''))
" 2>/dev/null)

# Kill each session sequentially with session_id in body (needed for worktree cleanup)
while IFS=' ' read -r sname sid; do
    sid="${sid%$'\r'}"   # Windows Python ends each printed line with CRLF; a CR in the JSON broke the kill body
    if [[ -n "$sname" && -n "$sid" ]]; then
        # A kill waits for the agent's graceful stop, up to ~10 s on Windows ConPTY: more than api's -m 10.
        curl -s -m 30 -X POST "${BASE_URL}/api/sessions/live/${sname}/kill" -H "Content-Type: application/json" -d "{\"session_id\": \"$sid\"}" >/dev/null 2>&1
        sleep 1
    fi
done <<< "$agent_info"

# Give async worktree cleanup a moment
sleep 3

# ── Test 8: Worktree directory removed ───────────────────────────────

log "Test 8: Verify worktree directory cleaned up..."
if [[ ! -d "$WORKTREE_PATH" ]]; then
    pass "Worktree directory removed after kill"
else
    fail "Worktree directory still exists after kill: $WORKTREE_PATH"
fi

# ── Test 9: git worktree list no longer shows worktree ───────────────

log "Test 9: Verify git worktree list after cleanup..."
wt_after=$(git -C "$REPO_DIR" worktree list 2>/dev/null | grep -c "coral-team/" 2>/dev/null || true)
wt_after=${wt_after:-0}
if [[ "$wt_after" -eq 0 ]]; then
    pass "git worktree list no longer shows the team worktree"
else
    fail "git worktree list still shows the worktree after cleanup"
fi

# ── Summary ──────────────────────────────────────────────────────────

echo ""
echo "================================"
echo "  TEAMS TEST RESULTS"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo "================================"

coral_test_sandbox_check || FAIL=$((FAIL + 1))   # #4354: no real CLI reached, real ~/.claude unchanged
if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
