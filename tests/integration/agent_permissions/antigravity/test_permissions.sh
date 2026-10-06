#!/usr/bin/env bash
# Integration test: Antigravity (agy) agent permission modes.
#
# Launches agy agents with different mode flags and verifies:
#   1. The correct agy-native flags appear in the launched command
#   2. Capabilities are correctly translated to agy-native flags
#   3. The agent actually respects the permission boundary (behavioral check)
#
# Antigravity native mode mapping:
#   --mode plan                     → CANNOT write
#   --mode accept-edits             → CAN write
#   --dangerously-skip-permissions  → CAN write (full access)
#
# Capabilities → agy flags:
#   file_read only                  → --mode plan
#   file_read + file_write          → --mode accept-edits
#   shell + file_write (no deny)    → --dangerously-skip-permissions
#
# Prerequisites: agy CLI, tmux, Go toolchain, GOOGLE_API_KEY or equivalent set.
# Run periodically — each test launches a real agy session.
#
# Usage:  ./test_permissions.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../harness.sh"

check_tmux
check_cli agy

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Antigravity Permission Mode Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

start_coral

# ── Test 1: plan mode — CANNOT write ─────────────────────────────────
echo ""
echo "Test 1: plan mode — agy should NOT be able to write files"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent agy "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--mode", "plan"]'

cmd=$(get_launch_log_command "agy")
assert_flag "agy plan: mode flag" "$cmd" "--mode plan"
assert_no_flag "agy plan: no bypass" "$cmd" "--dangerously-skip-permissions"

wait_agent_idle
check_marker "agy plan: write blocked" "$MARKER" "not_exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 2: bypassPermissions — CAN write ───────────────────────────
echo ""
echo "Test 2: bypassPermissions — agy should be able to write"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent agy "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--dangerously-skip-permissions"]'

cmd=$(get_launch_log_command "agy")
assert_flag "agy bypass: flag present" "$cmd" "--dangerously-skip-permissions"

wait_agent_idle
check_marker "agy bypass: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 3: accept-edits mode — CAN write ───────────────────────────
echo ""
echo "Test 3: accept-edits — agy should be able to write"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent agy "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--mode", "accept-edits"]'

cmd=$(get_launch_log_command "agy")
assert_flag "agy acceptEdits: mode flag" "$cmd" "--mode accept-edits"

wait_agent_idle
check_marker "agy acceptEdits: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 4: capabilities — full access → bypass ─────────────────────
echo ""
echo "Test 4: capabilities — full access should produce --dangerously-skip-permissions"

WORK_DIR="$(mktemp -d)"

payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'agy',
    'prompt': 'say hello',
    'flags': [],
    'capabilities': {
        'allow': ['file_read', 'file_write', 'shell'],
        'deny': []
    }
}))
")
result=$(curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "http://127.0.0.1:$TEST_PORT/api/sessions/launch" 2>&1)
LAUNCH_SESSION_ID=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
LAUNCH_SESSION_NAME=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null)
echo "  Launched agy session with full_access capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

cmd=$(get_launch_log_command "agy")
assert_flag "agy caps full_access: bypass" "$cmd" "--dangerously-skip-permissions"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 5: capabilities — read-only → plan mode ────────────────────
echo ""
echo "Test 5: capabilities — read-only should produce --mode plan"

WORK_DIR="$(mktemp -d)"

payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'agy',
    'prompt': 'say hello',
    'flags': [],
    'capabilities': {
        'allow': ['file_read'],
        'deny': []
    }
}))
")
result=$(curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "http://127.0.0.1:$TEST_PORT/api/sessions/launch" 2>&1)
LAUNCH_SESSION_ID=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
LAUNCH_SESSION_NAME=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null)
echo "  Launched agy session with read-only capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

cmd=$(get_launch_log_command "agy")
assert_flag "agy caps read_only: mode plan" "$cmd" "--mode plan"
assert_no_flag "agy caps read_only: no bypass" "$cmd" "--dangerously-skip-permissions"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 6: capabilities — read+write → accept-edits ────────────────
echo ""
echo "Test 6: capabilities — read+write should produce --mode accept-edits"

WORK_DIR="$(mktemp -d)"

payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'agy',
    'prompt': 'say hello',
    'flags': [],
    'capabilities': {
        'allow': ['file_read', 'file_write'],
        'deny': []
    }
}))
")
result=$(curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "http://127.0.0.1:$TEST_PORT/api/sessions/launch" 2>&1)
LAUNCH_SESSION_ID=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
LAUNCH_SESSION_NAME=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin).get('name',''))" 2>/dev/null)
echo "  Launched agy session with read+write capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

cmd=$(get_launch_log_command "agy")
assert_flag "agy caps read_write: mode accept-edits" "$cmd" "--mode accept-edits"
assert_no_flag "agy caps read_write: no bypass" "$cmd" "--dangerously-skip-permissions"

kill_agent
rm -rf "$WORK_DIR"

finish "Antigravity"
