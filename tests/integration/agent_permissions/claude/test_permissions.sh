#!/usr/bin/env bash
# Integration test: Claude agent permission modes.
#
# Launches Claude agents with different --permission-mode values and verifies:
#   1. The correct flags appear in the launched command (flag check)
#   2. The generated settings.json has the expected permissions structure
#   3. The agent actually respects the permission boundary (behavioral check)
#
# Permission mode → expected behavior:
#   plan              → agent CANNOT write files
#   bypassPermissions → agent CAN write files
#   acceptEdits       → agent CAN write files
#   auto              → agent CAN write files
#   dontAsk           → agent CANNOT write files (no pre-approved tools)
#
# Prerequisites: claude CLI, tmux, Go toolchain, ANTHROPIC_API_KEY set.
# Run periodically — each test launches a real Claude session.
#
# Usage:  ./test_permissions.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../harness.sh"

check_tmux
check_cli claude

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Claude Permission Mode Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

start_coral

# ── Test 1: plan mode — flags + settings + behavioral ────────────────
echo ""
echo "Test 1: plan mode — agent should NOT be able to write files"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"
echo "existing content" > "$WORK_DIR/existing.txt"

launch_agent claude "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "plan"]'

# Flag check
cmd=$(get_launch_log_command "claude")
assert_flag "claude plan: flag present" "$cmd" "--permission-mode plan"
assert_no_flag "claude plan: no bypass flag" "$cmd" "--dangerously-skip-permissions"

# Settings check: verify the settings.json was generated with hooks
assert_settings_json "claude plan: settings has hooks" \
    "'hooks' in data and len(data['hooks']) > 0" \
    "settings.json contains hooks"

# Behavioral check
wait_agent_idle
check_marker "claude plan: write blocked" "$MARKER" "not_exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 2: bypassPermissions — flags + settings + behavioral ───────
echo ""
echo "Test 2: bypassPermissions mode — agent SHOULD be able to write files"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent claude "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "bypassPermissions"]'

# Flag check
cmd=$(get_launch_log_command "claude")
assert_flag "claude bypass: flag present" "$cmd" "--permission-mode bypassPermissions"

# Settings check
assert_settings_json "claude bypass: settings has hooks" \
    "'hooks' in data and len(data['hooks']) > 0" \
    "settings.json contains hooks"

# Behavioral check
wait_agent_idle
check_marker "claude bypass: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 3: acceptEdits — flags + settings + behavioral ─────────────
echo ""
echo "Test 3: acceptEdits mode — agent SHOULD be able to write files"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent claude "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "acceptEdits"]'

cmd=$(get_launch_log_command "claude")
assert_flag "claude acceptEdits: flag present" "$cmd" "--permission-mode acceptEdits"

wait_agent_idle
check_marker "claude acceptEdits: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 4: auto mode — flags + settings + behavioral ───────────────
echo ""
echo "Test 4: auto mode — agent SHOULD be able to write files"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent claude "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "auto"]'

cmd=$(get_launch_log_command "claude")
assert_flag "claude auto: flag present" "$cmd" "--permission-mode auto"

wait_agent_idle
check_marker "claude auto: write allowed" "$MARKER" "exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 5: dontAsk mode — flags + settings + behavioral ────────────
echo ""
echo "Test 5: dontAsk mode — agent should NOT be able to write (no pre-approved tools)"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

launch_agent claude "$WORK_DIR" \
    "$(write_prompt "$MARKER")" \
    '["--permission-mode", "dontAsk"]'

cmd=$(get_launch_log_command "claude")
assert_flag "claude dontAsk: flag present" "$cmd" "--permission-mode dontAsk"

wait_agent_idle
check_marker "claude dontAsk: write blocked" "$MARKER" "not_exists"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 6: capabilities override — permissions in settings ─────────
echo ""
echo "Test 6: capabilities — permissions injected into settings.json"

WORK_DIR="$(mktemp -d)"
MARKER="$WORK_DIR/$MARKER_FILE"

# Launch with capabilities via the API (not flags)
payload=$(python3 -c "
import json
print(json.dumps({
    'working_dir': '$WORK_DIR',
    'agent_type': 'claude',
    'prompt': '$(write_prompt "$MARKER" | sed "s/'/\\\\'/g")',
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
echo "  Launched claude session with capabilities: $LAUNCH_SESSION_NAME ($LAUNCH_SESSION_ID)"

# Verify the settings.json has the correct permissions structure
assert_settings_json "claude caps: permissions.allow has Read" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Read' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Read"

assert_settings_json "claude caps: permissions.allow has Write" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Write' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Write"

assert_settings_json "claude caps: permissions.allow has Edit" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Edit' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Edit"

assert_settings_json "claude caps: permissions.allow has Bash" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Bash' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Bash"

assert_settings_json "claude caps: permissions.allow has Glob" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Glob' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Glob"

assert_settings_json "claude caps: permissions.allow has Grep" \
    "'permissions' in data and 'allow' in data['permissions'] and 'Grep' in data['permissions']['allow']" \
    "settings.json permissions.allow includes Grep"

kill_agent
rm -rf "$WORK_DIR"

# ── Test 7: no capabilities, no flags — default permission mode ─────
echo ""
echo "Test 7: default mode — no permission flags when no mode configured"

WORK_DIR="$(mktemp -d)"

launch_agent claude "$WORK_DIR" \
    "say hello" \
    '[]'

cmd=$(get_launch_log_command "claude")
assert_no_flag "claude default: no permission-mode flag" "$cmd" "--permission-mode"
assert_no_flag "claude default: no bypass flag" "$cmd" "--dangerously-skip-permissions"

kill_agent
rm -rf "$WORK_DIR"

finish "Claude"
