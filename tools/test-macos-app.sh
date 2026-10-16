#!/bin/bash
# Smoke test for the built Coral.app macOS bundle.
# Verifies the app starts, HTTP server responds, and nothing crashes.
#
# Usage:
#   scripts/test-macos-app.sh [path-to-Coral.app]
#   TEST_PORT=9999 scripts/test-macos-app.sh
#   SMOKE_TEST_WEBVIEW=1 scripts/test-macos-app.sh  # force webview test
#
# Default: installers/dist/Coral.app
# Exit codes: 0 = pass, 1 = fail

set -euo pipefail

# ── Phase 1: Setup ──────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

APP="${1:-$PROJECT_DIR/installers/dist/Coral.app}"
TEST_PORT="${TEST_PORT:-8499}"
TIMEOUT="${TIMEOUT:-30}"
FAILURES=0
TRAY_PID=""
APP_PID=""
CORAL_HOME="$HOME/.coral"

# Color helpers
pass() { printf "\033[32m[PASS]\033[0m %s\n" "$*"; }
fail() { printf "\033[31m[FAIL]\033[0m %s\n" "$*"; FAILURES=$((FAILURES + 1)); }
info() { printf "\033[33m[INFO]\033[0m %s\n" "$*"; }

echo "========================================="
echo " Coral.app Smoke Test"
echo "========================================="
info "App path: $APP"
info "Test port: $TEST_PORT"
info "Timeout: ${TIMEOUT}s"
echo ""

# Validate Coral.app bundle
if [ ! -f "$APP/Contents/MacOS/coral-tray" ]; then
    fail "coral-tray binary not found at $APP/Contents/MacOS/coral-tray"
    echo ""
    echo "Build Coral.app first:"
    echo "  ./installers/build-macos.sh dev"
    exit 1
fi
pass "Coral.app bundle found with coral-tray binary"

# ── Cleanup trap ────────────────────────────────────────────────────
cleanup() {
    info "Cleaning up..."
    if [ -n "$TRAY_PID" ] && kill -0 "$TRAY_PID" 2>/dev/null; then
        kill "$TRAY_PID" 2>/dev/null || true
        wait "$TRAY_PID" 2>/dev/null || true
    fi
    if [ -n "$APP_PID" ] && kill -0 "$APP_PID" 2>/dev/null; then
        kill "$APP_PID" 2>/dev/null || true
        wait "$APP_PID" 2>/dev/null || true
    fi
    # Kill anything still on the test port
    lsof -ti :"$TEST_PORT" 2>/dev/null | xargs kill 2>/dev/null || true
}
trap cleanup EXIT

# ── Phase 2: Kill existing processes on test port ───────────────────
info "Killing any existing processes on port $TEST_PORT..."
pkill -f "coral-tray.*--port $TEST_PORT" 2>/dev/null || true
lsof -ti :"$TEST_PORT" 2>/dev/null | xargs kill 2>/dev/null || true
rm -f "$CORAL_HOME/tray.pid"
# Truncate logs so we only check for crashes from THIS run
: > "$CORAL_HOME/tray.log" 2>/dev/null || true
: > "$CORAL_HOME/app.log" 2>/dev/null || true
sleep 1

# ── Phase 3: Launch coral-tray ─────────────────────────────────────
echo ""
info "Launching coral-tray on port $TEST_PORT..."
"$APP/Contents/MacOS/coral-tray" --foreground --no-browser --debug --port "$TEST_PORT" --backend pty &
TRAY_PID=$!
info "coral-tray PID: $TRAY_PID"

# ── Phase 4: Wait for server ───────────────────────────────────────
echo ""
info "Waiting for HTTP server (up to ${TIMEOUT}s)..."
ELAPSED=0
SERVER_UP=0
while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
    # Check process is still alive
    if ! kill -0 "$TRAY_PID" 2>/dev/null; then
        wait "$TRAY_PID" 2>/dev/null
        EXIT_CODE=$?
        fail "coral-tray died after ${ELAPSED}s with exit code $EXIT_CODE"
        TRAY_PID=""
        break
    fi

    # /api/health needs no credential (the app polls it); every other route does since #3936.
    if curl -sf "http://localhost:$TEST_PORT/api/health" >/dev/null 2>&1; then
        SERVER_UP=1
        pass "HTTP server responded after ${ELAPSED}s"
        break
    fi

    sleep 1
    ELAPSED=$((ELAPSED + 1))
done

if [ "$SERVER_UP" -eq 0 ] && [ -n "$TRAY_PID" ]; then
    fail "HTTP server did not respond within ${TIMEOUT}s"
fi

# ── Phase 5: API smoke checks ──────────────────────────────────────
echo ""
if [ "$SERVER_UP" -eq 1 ]; then
    info "Running API smoke checks..."

    # Strict local auth is on by default (#3936): a request with no credential is refused, and a
    # script sends the API key, which coral api-key reads from the data folder. Never printed.
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$TEST_PORT/api/system/status" 2>/dev/null || echo "000")
    if [ "$HTTP_CODE" = "401" ]; then
        pass "GET /api/system/status with no credential returned 401 (strict local auth)"
    else
        fail "GET /api/system/status with no credential returned $HTTP_CODE (expected 401)"
    fi
    API_KEY=$("$APP/Contents/MacOS/coral" api-key 2>/dev/null || true)
    if [ -n "$API_KEY" ]; then
        pass "coral api-key read the API key"
    else
        fail "coral api-key printed no key"
    fi
    AUTH=(-H "X-API-Key: $API_KEY")

    # GET /api/system/status -- response contains "startup_complete"
    STATUS_BODY=$(curl -sf "${AUTH[@]}" "http://localhost:$TEST_PORT/api/system/status" 2>/dev/null || echo "")
    if echo "$STATUS_BODY" | grep -q "startup_complete"; then
        pass "GET /api/system/status contains 'startup_complete'"
    else
        fail "GET /api/system/status missing 'startup_complete' (got: $STATUS_BODY)"
    fi

    # GET /api/sessions/live -- HTTP 200 (or 403 if license-gated in prod builds)
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${AUTH[@]}" "http://localhost:$TEST_PORT/api/sessions/live" 2>/dev/null || echo "000")
    if [ "$HTTP_CODE" = "200" ]; then
        pass "GET /api/sessions/live returned 200"
    elif [ "$HTTP_CODE" = "403" ]; then
        pass "GET /api/sessions/live returned 403 (license-gated, expected in prod builds)"
    else
        fail "GET /api/sessions/live returned $HTTP_CODE (expected 200 or 403)"
    fi

    # GET /api/settings -- HTTP 200 (or 403 if license-gated in prod builds)
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "${AUTH[@]}" "http://localhost:$TEST_PORT/api/settings" 2>/dev/null || echo "000")
    if [ "$HTTP_CODE" = "200" ]; then
        pass "GET /api/settings returned 200"
    elif [ "$HTTP_CODE" = "403" ]; then
        pass "GET /api/settings returned 403 (license-gated, expected in prod builds)"
    else
        fail "GET /api/settings returned $HTTP_CODE (expected 200 or 403)"
    fi
else
    info "Skipping API checks (server not up)"
fi

# ── Phase 6: Log inspection ────────────────────────────────────────
echo ""
info "Inspecting logs for crash signals..."
CRASH_PATTERN="SIGSEGV|SIGBUS|SIGABRT|panic:|fatal error:|signal: segmentation violation|runtime error:"
CRASH_FOUND=0

for LOG_FILE in "$CORAL_HOME/tray.log" "$CORAL_HOME/app.log"; do
    if [ -f "$LOG_FILE" ]; then
        if grep -iE "$CRASH_PATTERN" "$LOG_FILE" 2>/dev/null; then
            fail "Crash signal found in $(basename "$LOG_FILE")"
            CRASH_FOUND=1
        fi
    fi
done

if [ "$CRASH_FOUND" -eq 0 ]; then
    pass "No crash signals in logs"
fi

# ── Phase 7: Webview test ──────────────────────────────────────────
echo ""
RUN_WEBVIEW=0
if [ "${SMOKE_TEST_WEBVIEW:-}" = "1" ]; then
    RUN_WEBVIEW=1
elif [ -z "${CI:-}" ]; then
    RUN_WEBVIEW=1
fi

if [ "$RUN_WEBVIEW" -eq 1 ] && [ "$SERVER_UP" -eq 1 ]; then
    CORAL_APP_BIN="$APP/Contents/MacOS/coral-app"
    if [ -f "$CORAL_APP_BIN" ]; then
        info "Launching coral-app webview..."
        "$CORAL_APP_BIN" --url "http://localhost:$TEST_PORT" --debug &
        APP_PID=$!
        sleep 3

        if kill -0 "$APP_PID" 2>/dev/null; then
            pass "coral-app is running (PID $APP_PID)"
        else
            wait "$APP_PID" 2>/dev/null
            APP_EXIT=$?
            if [ "$APP_EXIT" -eq 2 ] || [ "$APP_EXIT" -gt 128 ]; then
                fail "coral-app crashed with exit code $APP_EXIT"
            else
                info "coral-app exited with code $APP_EXIT (may be expected in headless/CI)"
            fi
            APP_PID=""
        fi
    else
        info "coral-app binary not found, skipping webview test"
    fi
elif [ "$RUN_WEBVIEW" -eq 1 ]; then
    info "Skipping webview test (server not up)"
else
    info "Skipping webview test (CI mode, set SMOKE_TEST_WEBVIEW=1 to enable)"
fi

# ── Phase 8: Cleanup & report ──────────────────────────────────────
echo ""
echo "========================================="
if [ "$FAILURES" -eq 0 ]; then
    pass "All smoke tests passed"
else
    fail "$FAILURES test(s) failed"
    echo ""
    info "Last 30 lines of tray.log:"
    echo "-----------------------------------------"
    tail -30 "$CORAL_HOME/tray.log" 2>/dev/null || echo "(no tray.log)"
    echo "-----------------------------------------"
    info "Last 30 lines of app.log:"
    echo "-----------------------------------------"
    tail -30 "$CORAL_HOME/app.log" 2>/dev/null || echo "(no app.log)"
    echo "-----------------------------------------"
fi
echo "========================================="

if [ "$FAILURES" -gt 0 ]; then
    exit 1
else
    exit 0
fi
