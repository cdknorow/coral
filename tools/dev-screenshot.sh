#!/bin/bash
# Dev iteration script: build → start → screenshot
# Usage: ./scripts/dev-screenshot.sh
#
# Builds the Go server with dev tags, starts it on port 8450 with an isolated
# data dir, waits for health, opens the browser, and takes a screenshot.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CORAL_DIR="$SCRIPT_DIR/../coral-go"
BINARY="/tmp/coral-dev"
PORT=8450
HOME_DIR="/tmp/.coral-dev"
SCREENSHOT="/tmp/coral-screenshot.png"
HEALTH_URL="http://127.0.0.1:${PORT}/api/health"
DASH_URL="http://127.0.0.1:${PORT}"

# ── Build ────────────────────────────────────────────────────
echo "==> Building coral (dev tier)..."
cd "$CORAL_DIR"
go build -tags dev -o "$BINARY" ./cmd/coral/
echo "    Built: $BINARY"

# ── Kill existing ────────────────────────────────────────────
if lsof -ti :"$PORT" >/dev/null 2>&1; then
    echo "==> Killing existing process on port $PORT..."
    kill $(lsof -ti :"$PORT") 2>/dev/null || true
    sleep 1
fi

# ── Start server ─────────────────────────────────────────────
mkdir -p "$HOME_DIR"
echo "==> Starting server on port $PORT (home: $HOME_DIR)..."
# CORAL_NO_DEMO_SEED: the demo seed can launch a real agent CLI on PATH against the real ~/.claude
# (#4354); this dev helper takes a screenshot, it does not need the demo, so keep it from launching one.
CORAL_NO_DEMO_SEED=1 "$BINARY" --host 127.0.0.1 --port "$PORT" --home "$HOME_DIR" --no-browser &
SERVER_PID=$!
echo "    PID: $SERVER_PID"

# Cleanup on exit
trap "kill $SERVER_PID 2>/dev/null; exit" INT TERM EXIT

# ── Wait for health ──────────────────────────────────────────
echo "==> Waiting for health check..."
for i in $(seq 1 30); do
    if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
        echo "    Server healthy after ${i}s"
        break
    fi
    if [ "$i" -eq 30 ]; then
        echo "    ERROR: Server didn't start in 30s"
        exit 1
    fi
    sleep 1
done

# ── Open browser ─────────────────────────────────────────────
echo "==> Opening browser..."
open "$DASH_URL"
sleep 2

# Try to navigate to the first live session for a more useful screenshot
FIRST_SESSION=$(curl -sf "http://127.0.0.1:${PORT}/api/sessions/live" \
    | python3 -c "import sys,json; ss=json.load(sys.stdin); print(ss[0]['name'] if ss else '')" 2>/dev/null || true)
if [ -n "$FIRST_SESSION" ]; then
    echo "==> Navigating to session: $FIRST_SESSION"
    open "${DASH_URL}/#session=${FIRST_SESSION}"
fi

# ── Screenshot ───────────────────────────────────────────────
echo "==> Waiting 3s for page render..."
sleep 3

echo "==> Taking screenshot..."
screencapture -x "$SCREENSHOT"
echo "    Saved: $SCREENSHOT"

echo "==> Done. Server running on port $PORT (PID $SERVER_PID)"
echo "    Dashboard: $DASH_URL"
echo "    Screenshot: $SCREENSHOT"
echo "    Press Ctrl+C to stop"

# Keep running until interrupted
wait $SERVER_PID
