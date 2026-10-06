#!/usr/bin/env bash
# Integration test: Mixed-provider team permission mode translation.
#
# Launches a team with claude, codex, and agy agents sharing the same
# permission mode, and verifies each agent receives the correct NATIVE
# flags for its CLI — not the raw --permission-mode flag that only Claude
# understands.
#
# This is the test that catches CLI drift: when a provider changes its
# flag names or semantics, the native-flag assertions fail.
#
# ┌──────────────────┬───────────────────────────────────────────────────┐
# │ Coral mode       │ Expected native flags per provider               │
# ├──────────────────┼─────────────┬─────────────────┬─────────────────┤
# │                  │ Claude      │ Codex           │ Antigravity     │
# ├──────────────────┼─────────────┼─────────────────┼─────────────────┤
# │ bypassPermissions│ --permission│ --dangerously-  │ --dangerously-  │
# │                  │ -mode       │ bypass-approvals│ skip-permissions│
# │                  │ bypassPerm..│ -and-sandbox    │                 │
# ├──────────────────┼─────────────┼─────────────────┼─────────────────┤
# │ plan             │ --permission│ --sandbox       │ --mode plan     │
# │                  │ -mode plan  │ read-only       │                 │
# │                  │             │ -a on-request   │                 │
# ├──────────────────┼─────────────┼─────────────────┼─────────────────┤
# │ auto             │ --permission│ --sandbox       │ --dangerously-  │
# │                  │ -mode auto  │ workspace-write │ skip-permissions│
# │                  │             │ -a never        │                 │
# ├──────────────────┼─────────────┼─────────────────┼─────────────────┤
# │ acceptEdits      │ --permission│ --sandbox       │ --mode          │
# │                  │ -mode       │ workspace-write │ accept-edits    │
# │                  │ acceptEdits │ -a on-request   │                 │
# └──────────────────┴─────────────┴─────────────────┴─────────────────┘
#
# Prerequisites: claude, codex, agy CLIs; tmux; Go toolchain; API keys.
#
# Usage:  ./test_permissions.sh

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../harness.sh"

check_tmux

# Check all three CLIs are available; skip if any is missing
HAVE_ALL=true
for cli in claude codex agy; do
    if ! command -v "$cli" >/dev/null 2>&1; then
        echo "SKIP: $cli not found on PATH (all three CLIs required for mixed team test)"
        HAVE_ALL=false
    fi
done
if [ "$HAVE_ALL" = false ]; then
    exit 0
fi
echo "Found all CLIs: claude=$(command -v claude), codex=$(command -v codex), agy=$(command -v agy)"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Mixed Team Permission Mode Tests"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

start_coral

# Helper: launch a mixed team and return session names via the server log.
# Sets TEAM_CLAUDE_SESSION, TEAM_CODEX_SESSION, TEAM_AGY_SESSION.
launch_mixed_team() {
    local mode="$1"
    local work_dir="$2"

    # Build per-agent flags with the requested permission mode
    local perm_flags
    perm_flags=$(python3 -c "
import json
print(json.dumps(['--permission-mode', '$mode']))
")

    local payload
    payload=$(python3 -c "
import json, sys
flags = json.loads(sys.argv[1])
print(json.dumps({
    'board_name': 'perm-test-$mode',
    'working_dir': sys.argv[2],
    'agents': [
        {
            'name': 'Claude Agent',
            'agent_type': 'claude',
            'prompt': 'Say hello and stop.',
            'flags': flags,
        },
        {
            'name': 'Codex Agent',
            'agent_type': 'codex',
            'prompt': 'Say hello and stop.',
            'flags': flags,
        },
        {
            'name': 'Agy Agent',
            'agent_type': 'agy',
            'prompt': 'Say hello and stop.',
            'flags': flags,
        },
    ],
}))
" "$perm_flags" "$work_dir")

    local result
    result=$(curl -sf -X POST \
        -H "Content-Type: application/json" \
        -d "$payload" \
        "http://127.0.0.1:$TEST_PORT/api/sessions/launch-team" 2>&1) || {
        echo "  FAIL: launch-team request failed: $result"
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("launch-team ($mode): request failed")
        return 1
    }

    local error
    error=$(echo "$result" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error',''))" 2>/dev/null)
    if [ -n "$error" ]; then
        echo "  FAIL: launch-team error ($mode): $error"
        FAIL_COUNT=$((FAIL_COUNT + 1))
        FAILURES+=("launch-team ($mode): $error")
        return 1
    fi

    # Extract session names from the response.
    # The launch-team response has {agents: [{name, session_id, session_name}, ...]}.
    # Session names are prefixed with the agent type (e.g. "claude-<uuid>").
    TEAM_CLAUDE_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('claude-'):
        print(sn); break
" 2>/dev/null)
    TEAM_CODEX_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('codex-'):
        print(sn); break
" 2>/dev/null)
    TEAM_AGY_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('agy-'):
        print(sn); break
" 2>/dev/null)

    echo "  Launched mixed team ($mode):"
    echo "    claude: $TEAM_CLAUDE_SESSION"
    echo "    codex:  $TEAM_CODEX_SESSION"
    echo "    agy:    $TEAM_AGY_SESSION"

    # Give the server a moment to log all launch commands
    sleep 2
}

kill_team() {
    for session in "$TEAM_CLAUDE_SESSION" "$TEAM_CODEX_SESSION" "$TEAM_AGY_SESSION"; do
        if [ -n "$session" ]; then
            curl -sf -X DELETE "http://127.0.0.1:$TEST_PORT/api/sessions/live/${session}" >/dev/null 2>&1 || true
        fi
    done
    sleep 1
    for session in "$TEAM_CLAUDE_SESSION" "$TEAM_CODEX_SESSION" "$TEAM_AGY_SESSION"; do
        if [ -n "$session" ]; then
            tmux kill-session -t "$session" 2>/dev/null || true
        fi
    done
}

# ── Test 1: bypassPermissions team ──────────────────────────────────
echo ""
echo "Test 1: bypassPermissions — each provider gets its native bypass flag"

WORK_DIR="$(mktemp -d)"
launch_mixed_team "bypassPermissions" "$WORK_DIR"

# Claude: should have --permission-mode bypassPermissions (it's Claude's native format)
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "team bypass: claude gets --permission-mode bypassPermissions" \
    "$cmd" "--permission-mode bypassPermissions"

# Codex: should have --dangerously-bypass-approvals-and-sandbox (NOT --permission-mode)
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_flag "team bypass: codex gets native bypass" \
    "$cmd" "--dangerously-bypass-approvals-and-sandbox"
assert_no_flag "team bypass: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

# Agy: should have --dangerously-skip-permissions (NOT --permission-mode)
cmd=$(get_launch_log_command_by_session "$TEAM_AGY_SESSION")
assert_flag "team bypass: agy gets native bypass" \
    "$cmd" "--dangerously-skip-permissions"
assert_no_flag "team bypass: agy no raw --permission-mode" \
    "$cmd" "--permission-mode"

kill_team
rm -rf "$WORK_DIR"

# ── Test 2: plan team ───────────────────────────────────────────────
echo ""
echo "Test 2: plan — each provider gets its native read-only flag"

WORK_DIR="$(mktemp -d)"
launch_mixed_team "plan" "$WORK_DIR"

# Claude: --permission-mode plan
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "team plan: claude gets --permission-mode plan" \
    "$cmd" "--permission-mode plan"

# Codex: --sandbox read-only -a on-request
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_flag "team plan: codex gets sandbox read-only" \
    "$cmd" "--sandbox read-only"
assert_flag "team plan: codex gets approval on-request" \
    "$cmd" "-a on-request"
assert_no_flag "team plan: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

# Agy: --mode plan
cmd=$(get_launch_log_command_by_session "$TEAM_AGY_SESSION")
assert_flag "team plan: agy gets --mode plan" \
    "$cmd" "--mode plan"
assert_no_flag "team plan: agy no raw --permission-mode" \
    "$cmd" "--permission-mode"

kill_team
rm -rf "$WORK_DIR"

# ── Test 3: auto team ──────────────────────────────────────────────
echo ""
echo "Test 3: auto — each provider gets its native auto-approve flag"

WORK_DIR="$(mktemp -d)"
launch_mixed_team "auto" "$WORK_DIR"

# Claude: --permission-mode auto
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "team auto: claude gets --permission-mode auto" \
    "$cmd" "--permission-mode auto"

# Codex: --sandbox workspace-write -a never
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_flag "team auto: codex gets sandbox workspace-write" \
    "$cmd" "--sandbox workspace-write"
assert_flag "team auto: codex gets approval never" \
    "$cmd" "-a never"
assert_no_flag "team auto: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

# Agy: --dangerously-skip-permissions (auto = full access for agy)
cmd=$(get_launch_log_command_by_session "$TEAM_AGY_SESSION")
assert_flag "team auto: agy gets native bypass" \
    "$cmd" "--dangerously-skip-permissions"
assert_no_flag "team auto: agy no raw --permission-mode" \
    "$cmd" "--permission-mode"

kill_team
rm -rf "$WORK_DIR"

# ── Test 4: acceptEdits team ───────────────────────────────────────
echo ""
echo "Test 4: acceptEdits — each provider gets its native edit-approval flag"

WORK_DIR="$(mktemp -d)"
launch_mixed_team "acceptEdits" "$WORK_DIR"

# Claude: --permission-mode acceptEdits
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "team acceptEdits: claude gets --permission-mode acceptEdits" \
    "$cmd" "--permission-mode acceptEdits"

# Codex: --sandbox workspace-write -a on-request
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_flag "team acceptEdits: codex gets sandbox workspace-write" \
    "$cmd" "--sandbox workspace-write"
assert_flag "team acceptEdits: codex gets approval on-request" \
    "$cmd" "-a on-request"
assert_no_flag "team acceptEdits: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

# Agy: --mode accept-edits
cmd=$(get_launch_log_command_by_session "$TEAM_AGY_SESSION")
assert_flag "team acceptEdits: agy gets --mode accept-edits" \
    "$cmd" "--mode accept-edits"
assert_no_flag "team acceptEdits: agy no raw --permission-mode" \
    "$cmd" "--permission-mode"

kill_team
rm -rf "$WORK_DIR"

# ── Test 5: mixed permissions — each agent gets a different mode ─────
echo ""
echo "Test 5: mixed permissions — orchestrator bypass, worker plan, worker acceptEdits"

WORK_DIR="$(mktemp -d)"

payload=$(python3 -c "
import json
print(json.dumps({
    'board_name': 'perm-test-mixed-modes',
    'working_dir': '$WORK_DIR',
    'agents': [
        {
            'name': 'Orchestrator',
            'agent_type': 'claude',
            'prompt': 'Say hello and stop.',
            'flags': ['--permission-mode', 'bypassPermissions'],
        },
        {
            'name': 'Read-Only Worker',
            'agent_type': 'codex',
            'prompt': 'Say hello and stop.',
            'flags': ['--permission-mode', 'plan'],
        },
        {
            'name': 'Edit Worker',
            'agent_type': 'agy',
            'prompt': 'Say hello and stop.',
            'flags': ['--permission-mode', 'acceptEdits'],
        },
    ],
}))
")

result=$(curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "http://127.0.0.1:$TEST_PORT/api/sessions/launch-team" 2>&1) || {
    echo "  FAIL: launch-team request failed: $result"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    FAILURES+=("launch-team (mixed modes): request failed")
}

TEAM_CLAUDE_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('claude-'):
        print(sn); break
" 2>/dev/null)
TEAM_CODEX_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('codex-'):
        print(sn); break
" 2>/dev/null)
TEAM_AGY_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('agy-'):
        print(sn); break
" 2>/dev/null)

echo "  Launched mixed-permission team:"
echo "    claude (bypass):      $TEAM_CLAUDE_SESSION"
echo "    codex  (plan):        $TEAM_CODEX_SESSION"
echo "    agy    (acceptEdits): $TEAM_AGY_SESSION"
sleep 2

# Claude: bypassPermissions → --permission-mode bypassPermissions
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "mixed perms: claude gets bypassPermissions" \
    "$cmd" "--permission-mode bypassPermissions"
assert_no_flag "mixed perms: claude no plan" \
    "$cmd" "--permission-mode plan"

# Codex: plan → --sandbox read-only -a on-request (NOT bypass)
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_flag "mixed perms: codex gets sandbox read-only" \
    "$cmd" "--sandbox read-only"
assert_flag "mixed perms: codex gets approval on-request" \
    "$cmd" "-a on-request"
assert_no_flag "mixed perms: codex no bypass" \
    "$cmd" "--dangerously-bypass-approvals-and-sandbox"
assert_no_flag "mixed perms: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

# Agy: acceptEdits → --mode accept-edits (NOT bypass)
cmd=$(get_launch_log_command_by_session "$TEAM_AGY_SESSION")
assert_flag "mixed perms: agy gets --mode accept-edits" \
    "$cmd" "--mode accept-edits"
assert_no_flag "mixed perms: agy no bypass" \
    "$cmd" "--dangerously-skip-permissions"
assert_no_flag "mixed perms: agy no raw --permission-mode" \
    "$cmd" "--permission-mode"

kill_team
rm -rf "$WORK_DIR"

# ── Test 6: team-level fallback — per-agent flags override team flags ─
echo ""
echo "Test 6: team-level fallback — agent with flags ignores team flags"

WORK_DIR="$(mktemp -d)"

# Team-level flags say plan, but the claude agent overrides to bypassPermissions
payload=$(python3 -c "
import json
print(json.dumps({
    'board_name': 'perm-test-override',
    'working_dir': '$WORK_DIR',
    'flags': ['--permission-mode', 'plan'],
    'agents': [
        {
            'name': 'Override Agent',
            'agent_type': 'claude',
            'prompt': 'Say hello and stop.',
            'flags': ['--permission-mode', 'bypassPermissions'],
        },
        {
            'name': 'Fallback Agent',
            'agent_type': 'codex',
            'prompt': 'Say hello and stop.',
        },
    ],
}))
")

result=$(curl -sf -X POST \
    -H "Content-Type: application/json" \
    -d "$payload" \
    "http://127.0.0.1:$TEST_PORT/api/sessions/launch-team" 2>&1) || {
    echo "  FAIL: launch-team request failed: $result"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    FAILURES+=("launch-team (override): request failed")
}

TEAM_CLAUDE_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('claude-'):
        print(sn); break
" 2>/dev/null)
TEAM_CODEX_SESSION=$(echo "$result" | python3 -c "
import sys, json
data = json.load(sys.stdin)
for a in data.get('agents', []):
    sn = a.get('session_name', '')
    if sn.startswith('codex-'):
        print(sn); break
" 2>/dev/null)

echo "  Launched override team:"
echo "    claude (per-agent bypass): $TEAM_CLAUDE_SESSION"
echo "    codex  (team-level plan):  $TEAM_CODEX_SESSION"
sleep 2

# Claude: per-agent flags win → bypassPermissions (not team-level plan)
cmd=$(get_launch_log_command_by_session "$TEAM_CLAUDE_SESSION")
assert_flag "override: claude gets bypassPermissions from per-agent flags" \
    "$cmd" "--permission-mode bypassPermissions"
assert_no_flag "override: claude no plan" \
    "$cmd" "--permission-mode plan"

# Codex: no per-agent flags → falls back to team-level plan
# Team-level --permission-mode is stripped by stripAgentPermissionFlags,
# so codex falls through to the global default_permission_mode (empty for test server)
cmd=$(get_launch_log_command_by_session "$TEAM_CODEX_SESSION")
assert_no_flag "override: codex no bypass" \
    "$cmd" "--dangerously-bypass-approvals-and-sandbox"
assert_no_flag "override: codex no raw --permission-mode" \
    "$cmd" "--permission-mode"

for session in "$TEAM_CLAUDE_SESSION" "$TEAM_CODEX_SESSION"; do
    if [ -n "$session" ]; then
        curl -sf -X DELETE "http://127.0.0.1:$TEST_PORT/api/sessions/live/${session}" >/dev/null 2>&1 || true
    fi
done
sleep 1
for session in "$TEAM_CLAUDE_SESSION" "$TEAM_CODEX_SESSION"; do
    if [ -n "$session" ]; then
        tmux kill-session -t "$session" 2>/dev/null || true
    fi
done
rm -rf "$WORK_DIR"

finish "Mixed Team"
