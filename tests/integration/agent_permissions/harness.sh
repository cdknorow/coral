#!/usr/bin/env bash
# Shared harness for agent permission integration tests.
# Source this from each agent-type test script.
#
# Provides:
#   start_coral             — build & start an isolated test server
#   stop_coral              — tear down server and agent sessions
#   launch_agent            — POST /api/sessions/launch; sets LAUNCH_SESSION_ID
#   wait_agent_idle         — poll until the agent stops producing output
#   check_marker_file       — verify a marker file exists (or doesn't)
#   assert_flag / assert_no_flag — check server log for flags
#   assert_settings_json    — verify the generated settings.json contains expected values
#   dump_agent_output       — capture and display agent terminal output
#   finish                  — print summary and exit

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CORAL_GO="$PROJECT_ROOT/coral-go"

TEST_PORT="${CORAL_TEST_PORT:-8451}"
TEST_DATA_DIR=""
CORAL_PID=""
CORAL_LOG=""
BINARY=""

PASS_COUNT=0
FAIL_COUNT=0
FAILURES=()

# Maximum seconds to wait for an agent to process its prompt
AGENT_TIMEOUT="${AGENT_TIMEOUT:-90}"

# Minimum seconds to wait before starting idle detection (gives the agent time to start)
AGENT_MIN_WAIT="${AGENT_MIN_WAIT:-15}"

# ── Server lifecycle ──────────────────────────────────────────────────

start_coral() {
    # Check port is free
    if lsof -i ":$TEST_PORT" -sTCP:LISTEN >/dev/null 2>&1; then
        echo "FATAL: port $TEST_PORT is already in use"
        exit 1
    fi

    TEST_DATA_DIR="$(mktemp -d)"
    CORAL_LOG="$TEST_DATA_DIR/coral.log"
    BINARY="$TEST_DATA_DIR/coral"

    echo "Building coral (dev mode)..."
    (cd "$CORAL_GO" && go build -tags dev -o "$BINARY" ./cmd/coral/) || {
        echo "FATAL: build failed"
        exit 1
    }

    echo "Starting coral on port $TEST_PORT (data: $TEST_DATA_DIR)..."
    CORAL_DATA_DIR="$TEST_DATA_DIR" "$BINARY" \
        --host 127.0.0.1 --port "$TEST_PORT" \
        >"$CORAL_LOG" 2>&1 &
    CORAL_PID=$!

    local attempts=0
    while ! curl -sf "http://127.0.0.1:$TEST_PORT/api/system/status" >/dev/null 2>&1; do
        attempts=$((attempts + 1))
        if [ $attempts -ge 30 ]; then
            echo "FATAL: coral did not become healthy after 30s"
            tail -20 "$CORAL_LOG"
            kill "$CORAL_PID" 2>/dev/null || true
            exit 1
        fi
        sleep 1
    done
    echo "Coral is healthy (pid $CORAL_PID)"
}

stop_coral() {
    echo ""
    echo "Cleaning up..."

    # Kill agent tmux sessions created during tests
    for prefix in claude codex agy; do
        tmux list-sessions -F '#{session_name}' 2>/dev/null \
            | grep "^${prefix}-" \
            | while read -r s; do
                tmux kill-session -t "$s" 2>/dev/null || true
            done
    done

    if [ -n "$CORAL_PID" ] && kill -0 "$CORAL_PID" 2>/dev/null; then
        kill "$CORAL_PID" 2>/dev/null || true
        wait "$CORAL_PID" 2>/dev/null || true
    fi

    if [ -n "$TEST_DATA_DIR" ] && [ -d "$TEST_DATA_DIR" ]; then
        rm -rf "$TEST_DATA_DIR"
    fi
}

trap stop_coral EXIT

# ── Launch helpers ────────────────────────────────────────────────────

LAUNCH_SESSION_ID=""
LAUNCH_SESSION_NAME=""

# Launch a single agent via /api/sessions/launch.
#   $1 = agent_type (claude, codex, agy)
#   $2 = working_dir
#   $3 = prompt
#   $4... = flags as JSON array string, e.g. '["--permission-mode","plan"]'
launch_agent() {
    local agent_type="$1"
    local work_dir="$2"
    local prompt="$3"
    local flags="${4:-[]}"

    local payload
    payload=$(python3 -c "
import json, sys
print(json.dumps({
    'working_dir': sys.argv[1],
    'agent_type': sys.argv[2],
    'prompt': sys.argv[3],
    'flags': json.loads(sys.argv[4]),
}))
" "$work_dir" "$agent_type" "$prompt" "$flags")

    local result
    result=$(curl -sf -X POST \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "http://127.0.0.1:$TEST_PORT/api/sessions/launch" 2>&1) || {
        echo "FAIL: launch request failed: $result"
        return 1
    }

    local error
    error=$(echo "$result" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',''))" 2>/dev/null)
    if [ -n "$error" ]; then
        echo "FAIL: launch error: $error"
        return 1
    fi

    LAUNCH_SESSION_ID=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
    LAUNCH_SESSION_NAME=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null)

    if [ -z "$LAUNCH_SESSION_ID" ]; then
        echo "FAIL: no session_id in response: $result"
        return 1
    fi
    echo "  Launched $agent_type session: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"
}

# Launch a team via /api/sessions/launch-team (for mixed-agent tests).
#   $1 = JSON payload
launch_team() {
    local payload="$1"
    local result
    result=$(curl -sf -X POST \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "http://127.0.0.1:$TEST_PORT/api/sessions/launch-team" 2>&1) || {
        echo "FAIL: launch-team request failed: $result"
        return 1
    }

    local error
    error=$(echo "$result" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',''))" 2>/dev/null)
    if [ -n "$error" ]; then
        echo "FAIL: launch-team error: $error"
        return 1
    fi
    echo "$result"
}

# Wait for an agent to go idle (output stops changing).
# Polls the capture endpoint; returns when output stabilizes for 5s.
# Waits at least AGENT_MIN_WAIT seconds before starting idle detection
# to avoid false-early exits during agent startup.
wait_agent_idle() {
    local session_name="${1:-$LAUNCH_SESSION_NAME}"
    local timeout="${2:-$AGENT_TIMEOUT}"
    local min_wait="${AGENT_MIN_WAIT}"
    local elapsed=0
    local prev_hash=""
    local stable_count=0

    echo "  Waiting for agent to finish (min ${min_wait}s, timeout ${timeout}s)..."

    # Phase 1: wait minimum time without checking idle (let the agent start)
    while [ $elapsed -lt "$min_wait" ] && [ $elapsed -lt "$timeout" ]; do
        sleep 3
        elapsed=$((elapsed + 3))
    done

    # Phase 2: poll for idle (output stabilizes for 3 consecutive checks = 9s)
    while [ $elapsed -lt "$timeout" ]; do
        local capture
        capture=$(curl -sf "http://127.0.0.1:$TEST_PORT/api/sessions/live/${session_name}/capture" 2>/dev/null || echo "")
        local hash
        hash=$(echo "$capture" | md5sum 2>/dev/null | cut -d' ' -f1 || echo "$capture" | md5 2>/dev/null || echo "none")

        if [ "$hash" = "$prev_hash" ]; then
            stable_count=$((stable_count + 1))
            if [ $stable_count -ge 3 ]; then
                echo "  Agent idle after ${elapsed}s"
                return 0
            fi
        else
            stable_count=0
            prev_hash="$hash"
        fi

        sleep 3
        elapsed=$((elapsed + 3))
    done
    echo "  WARNING: agent did not idle within ${timeout}s"
    return 0  # don't fail — the marker check is the real assertion
}

# Kill the current agent's tmux session
kill_agent() {
    local session_name="${1:-$LAUNCH_SESSION_NAME}"
    if [ -n "$session_name" ]; then
        # Use the Coral API to kill the session cleanly
        curl -sf -X DELETE "http://127.0.0.1:$TEST_PORT/api/sessions/live/${session_name}" >/dev/null 2>&1 || true
        sleep 1
        # Belt-and-suspenders: also kill the tmux session directly
        tmux kill-session -t "$session_name" 2>/dev/null || true
    fi
}

# ── Agent output capture ─────────────────────────────────────────────

# Dump the last N lines of agent terminal output (for debugging failures).
dump_agent_output() {
    local session_name="${1:-$LAUNCH_SESSION_NAME}"
    local label="${2:-agent}"
    local capture
    capture=$(curl -sf "http://127.0.0.1:$TEST_PORT/api/sessions/live/${session_name}/capture" 2>/dev/null || echo "(no capture available)")
    echo ""
    echo "  ── $label terminal output (last 30 lines) ──"
    echo "$capture" | tail -30 | sed 's/^/    /'
    echo "  ── end output ──"
    echo ""
}

# ── Marker file helpers ──────────────────────────────────────────────

MARKER_FILE="perm_test_marker.txt"
MARKER_CONTENT="CORAL_PERMISSION_TEST_OK"

# Build the standard prompt that asks the agent to write a marker file.
write_prompt() {
    local marker_path="$1"
    cat <<EOF
You are being tested. Your ONLY job is to write a file. Do not do anything else.

Write the exact text "$MARKER_CONTENT" to the file: $marker_path

Use whatever tool is available to write the file (Write tool, Bash tool with echo, etc).
Do NOT ask for confirmation. Do NOT explain what you are doing. Just write the file.
If you cannot write the file because of permission restrictions, just say "BLOCKED" and stop.
EOF
}

# Check whether the marker file was created.
#   $1 = test name
#   $2 = path to marker file
#   $3 = "exists" or "not_exists"
check_marker() {
    local test_name="$1"
    local marker_path="$2"
    local expectation="$3"

    if [ "$expectation" = "exists" ]; then
        if [ -f "$marker_path" ]; then
            local content
            content=$(cat "$marker_path" 2>/dev/null || echo "")
            if echo "$content" | grep -qF "$MARKER_CONTENT"; then
                PASS_COUNT=$((PASS_COUNT + 1))
                echo "  PASS: $test_name — marker file created with correct content"
            else
                PASS_COUNT=$((PASS_COUNT + 1))
                echo "  PASS: $test_name — marker file created (content: '${content:0:50}')"
            fi
        else
            FAIL_COUNT=$((FAIL_COUNT + 1))
            FAILURES+=("$test_name: expected marker file to exist but it was not created")
            echo "  FAIL: $test_name — marker file NOT created (expected it to exist)"
            dump_agent_output "$LAUNCH_SESSION_NAME" "$test_name"
        fi
    elif [ "$expectation" = "not_exists" ]; then
        if [ -f "$marker_path" ]; then
            FAIL_COUNT=$((FAIL_COUNT + 1))
            FAILURES+=("$test_name: marker file exists but should have been blocked by permissions")
            echo "  FAIL: $test_name — marker file WAS created (should have been blocked!)"
            dump_agent_output "$LAUNCH_SESSION_NAME" "$test_name"
        else
            PASS_COUNT=$((PASS_COUNT + 1))
            echo "  PASS: $test_name — marker file correctly NOT created (permissions enforced)"
        fi
    fi
}

# ── Flag assertions (from server log) ────────────────────────────────

get_launch_log_command() {
    local agent_type="$1"
    grep "\[launch\].*agent=$agent_type.*cmd=" "$CORAL_LOG" 2>/dev/null | tail -1 || echo ""
}

# Get the launch log command for a specific session (by session name).
# Useful in team launches where multiple agents of the same type exist.
get_launch_log_command_by_session() {
    local session_name="$1"
    grep "\[launch\] .* session=${session_name} .*cmd=" "$CORAL_LOG" 2>/dev/null | tail -1 || echo ""
}

assert_flag() {
    local test_name="$1"
    local haystack="$2"
    local flag="$3"

    if echo "$haystack" | grep -qF -- "$flag"; then
        PASS_COUNT=$((PASS_COUNT + 1))
        echo "  PASS: $test_name — found '$flag'"
    else
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("$test_name: expected '$flag' in command")
        echo "  FAIL: $test_name — '$flag' not found"
        echo "        command: $(echo "$haystack" | head -1 | cut -c1-200)"
    fi
}

assert_no_flag() {
    local test_name="$1"
    local haystack="$2"
    local flag="$3"

    if echo "$haystack" | grep -qF -- "$flag"; then
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("$test_name: unexpected '$flag' in command")
        echo "  FAIL: $test_name — '$flag' should not be present"
    else
        PASS_COUNT=$((PASS_COUNT + 1))
        echo "  PASS: $test_name — '$flag' correctly absent"
    fi
}

# ── Settings file assertions ─────────────────────────────────────────

# Find the settings temp file for a session from the server log.
#   $1 = session_id (or partial match)
get_settings_file() {
    local session_id="${1:-$LAUNCH_SESSION_ID}"
    grep "\[launch\].*cmd=" "$CORAL_LOG" 2>/dev/null \
        | tail -1 \
        | grep -oE '/[^ ]*coral_settings_[^ ]*\.json' \
        || echo ""
}

# Assert that the settings JSON file contains (or doesn't contain) a value.
#   $1 = test name
#   $2 = jq expression (e.g. '.permissions.allow | length > 0')
#   $3 = human-readable description of what we're checking
assert_settings_json() {
    local test_name="$1"
    local jq_expr="$2"
    local description="$3"

    local settings_file
    settings_file=$(get_settings_file)

    if [ -z "$settings_file" ] || [ ! -f "$settings_file" ]; then
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("$test_name: settings file not found")
        echo "  FAIL: $test_name — settings file not found for session"
        return
    fi

    if python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
# Evaluate the expression
expr = sys.argv[2]
result = eval(expr, {'data': data})
sys.exit(0 if result else 1)
" "$settings_file" "$jq_expr" 2>/dev/null; then
        PASS_COUNT=$((PASS_COUNT + 1))
        echo "  PASS: $test_name — $description"
    else
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("$test_name: $description — assertion failed")
        echo "  FAIL: $test_name — $description"
        echo "        settings: $(python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
# Show relevant keys
relevant = {}
for k in ['permissions', 'systemPrompt']:
    if k in data:
        v = data[k]
        if isinstance(v, str) and len(v) > 100:
            relevant[k] = v[:100] + '...'
        else:
            relevant[k] = v
print(json.dumps(relevant, indent=2))
" "$settings_file" 2>/dev/null || echo "(could not read)")"
    fi
}

# Dump the full settings file for debugging.
dump_settings() {
    local label="${1:-settings}"
    local settings_file
    settings_file=$(get_settings_file)

    if [ -z "$settings_file" ] || [ ! -f "$settings_file" ]; then
        echo "  (no settings file found)"
        return
    fi

    echo ""
    echo "  ── $label settings.json ──"
    python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
# Redact systemPrompt for brevity
if 'systemPrompt' in data:
    data['systemPrompt'] = data['systemPrompt'][:100] + '...' if len(data.get('systemPrompt','')) > 100 else data.get('systemPrompt','')
print(json.dumps(data, indent=2))
" "$settings_file" 2>/dev/null | head -40 | sed 's/^/    /'
    echo "  ── end settings ──"
}

# ── Summary ───────────────────────────────────────────────────────────

finish() {
    local agent_type="$1"
    echo ""
    echo "═══════════════════════════════════════════"
    echo "  $agent_type: $PASS_COUNT passed, $FAIL_COUNT failed"
    echo "═══════════════════════════════════════════"

    if [ $FAIL_COUNT -gt 0 ]; then
        echo ""
        echo "Failures:"
        for f in "${FAILURES[@]}"; do
            echo "  - $f"
        done
        echo ""
        echo "Server log (last 40 lines):"
        tail -40 "$CORAL_LOG"
        exit 1
    fi
    exit 0
}

# ── Prerequisite checks ──────────────────────────────────────────────

check_cli() {
    local cli="$1"
    if ! command -v "$cli" >/dev/null 2>&1; then
        echo "SKIP: $cli not found on PATH"
        exit 0
    fi
    echo "Found $cli: $(command -v "$cli")"
}

check_tmux() {
    if ! command -v tmux >/dev/null 2>&1; then
        echo "SKIP: tmux not found"
        exit 0
    fi
}
