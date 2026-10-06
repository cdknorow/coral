#!/usr/bin/env bash
# Integration test: Codex agent permission modes.
#
# Launches Codex agents with different permission flags and verifies:
#   1. The correct Codex-native flags appear in the launched command
#   2. Claude's --permission-mode values are correctly translated to Codex flags
#   3. The agent actually respects the permission boundary (behavioral check)
#
# Codex mode mapping (Coral translates --permission-mode to native Codex flags):
#   plan              → --sandbox read-only -a on-request  → CANNOT write
#   acceptEdits       → --sandbox workspace-write -a on-request → CAN write
#   auto              → --sandbox workspace-write -a never  → CAN write
#   bypassPermissions → --dangerously-bypass-approvals-and-sandbox → CAN write
#   dontAsk           → --sandbox workspace-write -a never  → CAN write (same as auto)
#
# Prerequisites: codex CLI, tmux, Go toolchain, OPENAI_API_KEY set.
# Run periodically — each test launches a real Codex session.
#
# Usage:  ./test_permissions.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../harness.sh"

check_tmux
check_cli codex

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Codex Permission Mode Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

start_coral

# ── Test 1: plan (read-only sandbox) — CANNOT write ──────────────────
echo ""
echo "Test 1: plan mode — Codex in read-only sandbox should NOT write"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent codex "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "plan"]'

cmd=$(get_launch_log_command "codex")
# Verify Coral correctly translated --permission-mode plan to native Codex flags
assert_flag "codex plan: sandbox read-only" "$cmd" "--sandbox read-only"
assert_flag "codex plan: approval on-request" "$cmd" "-a on-request"
assert_no_flag "codex plan: no bypass" "$cmd" "--dangerously-bypass-approvals-and-sandbox"
# The --permission-mode flag itself should NOT appear (it's Claude-specific)
assert_no_flag "codex plan: no raw permission-mode" "$cmd" "--permission-mode"

wait_agent_idle
check_marker "codex plan: write blocked" "$MARKER" "not_exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 2: bypassPermissions — CAN write ───────────────────────────
echo ""
echo "Test 2: bypassPermissions — Codex should be able to write"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent codex "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "bypassPermissions"]'

cmd=$(get_launch_log_command "codex")
# Verify Coral correctly translated to native Codex bypass flag
assert_flag "codex bypass: native bypass flag" "$cmd" "--dangerously-bypass-approvals-and-sandbox"
assert_no_flag "codex bypass: no raw permission-mode" "$cmd" "--permission-mode"

wait_agent_idle
check_marker "codex bypass: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 3: acceptEdits — CAN write within workspace ─────────────────
echo ""
echo "Test 3: acceptEdits — Codex workspace-write sandbox should allow writes"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent codex "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "acceptEdits"]'

cmd=$(get_launch_log_command "codex")
# Verify Coral correctly translated to native Codex flags
assert_flag "codex acceptEdits: sandbox workspace-write" "$cmd" "--sandbox workspace-write"
assert_flag "codex acceptEdits: approval on-request" "$cmd" "-a on-request"
assert_no_flag "codex acceptEdits: no raw permission-mode" "$cmd" "--permission-mode"

wait_agent_idle
check_marker "codex acceptEdits: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 4: auto — CAN write (never-ask approval) ───────────────────
echo ""
echo "Test 4: auto mode — Codex workspace-write with auto-approval"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent codex "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "auto"]'

cmd=$(get_launch_log_command "codex")
# Verify Coral correctly translated to native Codex flags
assert_flag "codex auto: sandbox workspace-write" "$cmd" "--sandbox workspace-write"
assert_flag "codex auto: approval never" "$cmd" "-a never"
assert_no_flag "codex auto: no raw permission-mode" "$cmd" "--permission-mode"

wait_agent_idle
check_marker "codex auto: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 5: capabilities override — verify native Codex flags ───────
echo ""
echo "Test 5: capabilities — verify Codex flag translation from capabilities"

WORK_DIR="$(mktemp -d)"

# Full access capabilities → should produce --dangerously-bypass-approvals-and-sandbox
payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'codex',
    'prompt': 'say hello',
    'flags': [],
    'capabilities': {
        'allow': ['file_read', 'file_write', 'shell', 'git_write'],
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
echo "  Launched codex session with full_access capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

cmd=$(get_launch_log_command "codex")
assert_flag "codex caps full_access: bypass sandbox" "$cmd" "--dangerously-bypass-approvals-and-sandbox"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 6: read-only capabilities ──────────────────────────────────
echo ""
echo "Test 6: capabilities — read-only should produce read-only sandbox"

WORK_DIR="$(mktemp -d)"

payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'codex',
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
echo "  Launched codex session with read-only capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

cmd=$(get_launch_log_command "codex")
assert_flag "codex caps read_only: sandbox read-only" "$cmd" "--sandbox read-only"
assert_flag "codex caps read_only: approval on-request" "$cmd" "-a on-request"
assert_no_flag "codex caps read_only: no bypass" "$cmd" "--dangerously-bypass-approvals-and-sandbox"

kill_agent
rm -rf "$WORK_DIR"

finish "Codex"
