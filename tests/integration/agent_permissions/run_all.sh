#!/usr/bin/env bash
# Run agent permission integration tests.
#
# Each agent type starts its own Coral server instance, launches agents with
# different permission modes, and verifies both the CLI flags AND actual
# behavioral enforcement (can/cannot write files).
#
# Prerequisites:
#   - tmux
#   - Go toolchain
#   - Agent CLIs installed (claude, codex, agy)
#   - API keys set (ANTHROPIC_API_KEY, OPENAI_API_KEY, GOOGLE_API_KEY)
#
# Usage:
#   ./run_all.sh                      # run all three
#   ./run_all.sh claude               # run just Claude tests
#   ./run_all.sh claude codex         # run Claude and Codex
#
# Environment:
#   CORAL_TEST_PORT=8451   Override the test server port
#   AGENT_TIMEOUT=90       Seconds to wait for each agent to process

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

SUITES=("claude" "codex" "antigravity")
if [ $# -gt 0 ]; then
    SUITES=("$@")
fi

OVERALL_PASS=0
OVERALL_FAIL=0
OVERALL_SKIP=0

for suite in "${SUITES[@]}"; do
    test_script="$SCRIPT_DIR/$suite/test_permissions.sh"
    if [ ! -f "$test_script" ]; then
        echo "ERROR: no test at $test_script"
        OVERALL_FAIL=$((OVERALL_FAIL + 1))
        continue
    fi

    echo ""
    echo "╔══════════════════════════════════════════╗"
    echo "║  Running: $suite"
    echo "╚══════════════════════════════════════════╝"
    echo ""

    if bash "$test_script"; then
        OVERALL_PASS=$((OVERALL_PASS + 1))
    else
        rc=$?
        if [ $rc -eq 0 ]; then
            OVERALL_SKIP=$((OVERALL_SKIP + 1))
        else
            OVERALL_FAIL=$((OVERALL_FAIL + 1))
        fi
    fi
done

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║  Overall: $OVERALL_PASS passed, $OVERALL_FAIL failed, $OVERALL_SKIP skipped"
echo "╚══════════════════════════════════════════╝"

[ $OVERALL_FAIL -eq 0 ]
