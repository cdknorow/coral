#!/usr/bin/env bash
# Boot an isolated Coral dev server + headless Chrome, run the frontend
# test suite against them, and clean up on exit.
#
# Requires:
#   - node (v18+) with the deps installed via `npm install` in this dir
#   - Google Chrome (macOS: /Applications/Google Chrome.app)
#   - A built dev coral binary (or CORAL_BIN env override)

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"

# NOTE: deliberately NOT ${CORAL_PORT}. Shells spawned by a running Coral
# instance inherit CORAL_PORT/CORAL_URL/CORAL_DATA_DIR pointing at production
# (:8420, ~/.coral). Honouring them here would aim the suite at production.
CORAL_PORT="${CORAL_TEST_PORT:-8462}"
CDP_PORT="${CDP_PORT:-9222}"
PROD_PORT=8420

if [ "$CORAL_PORT" = "$PROD_PORT" ] && [ -z "${CORAL_TEST_ALLOW_PROD:-}" ]; then
    echo "ERROR: refusing to run the frontend suite on :$PROD_PORT (production Coral port)." >&2
    echo "       Set CORAL_TEST_PORT to a free non-production port (or CORAL_TEST_ALLOW_PROD=1 to override deliberately)." >&2
    exit 2
fi

# Refuse a port that already has ANY listener (including a wildcard *:PORT
# bind). On macOS a 127.0.0.1:PORT listener can bind alongside a *:PORT one,
# silently splitting traffic between the two servers.
port_in_use() {
    if command -v lsof >/dev/null 2>&1; then
        [ -n "$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null)" ]
    else
        nc -z 127.0.0.1 "$1" >/dev/null 2>&1 || nc -z ::1 "$1" >/dev/null 2>&1
    fi
}
if port_in_use "$CORAL_PORT"; then
    echo "ERROR: something is already listening on :$CORAL_PORT; refusing to start the test server." >&2
    command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$CORAL_PORT" -sTCP:LISTEN >&2
    exit 2
fi
if port_in_use "$CDP_PORT"; then
    echo "ERROR: something is already listening on CDP port :$CDP_PORT; set CDP_PORT to a free port." >&2
    exit 2
fi
CORAL_HOME="$(mktemp -d /tmp/coral-frontend-test.XXXXXX)"
CHROME_PROFILE="$(mktemp -d /tmp/coral-frontend-chrome.XXXXXX)"
# Isolated Codex home: suites write rollout fixtures here and the test server
# reads Codex transcripts from it, never from the real ~/.codex.
export CODEX_HOME="$CORAL_HOME/codex"
mkdir -p "$CODEX_HOME/sessions"

CORAL_BIN="${CORAL_BIN:-}"
if [ -z "$CORAL_BIN" ]; then
    CORAL_BIN="$(mktemp -u /tmp/coral-frontend-test-bin.XXXXXX)"
    echo "Building dev coral binary at $CORAL_BIN..."
    (cd "$ROOT/coral-go" && go build -tags dev -o "$CORAL_BIN" ./cmd/coral/)
fi

# Pick a Chrome binary (macOS path, then PATH fallback).
CHROME="${CHROME:-}"
if [ -z "$CHROME" ]; then
    if [ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]; then
        CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    else
        CHROME="$(command -v google-chrome || command -v chromium || true)"
    fi
fi
if [ -z "$CHROME" ]; then
    echo "ERROR: could not find Chrome. Set CHROME=/path/to/chrome" >&2
    exit 2
fi

CORAL_PID=""
CHROME_PID=""
cleanup() {
    [ -n "$CORAL_PID" ] && kill "$CORAL_PID" 2>/dev/null || true
    [ -n "$CHROME_PID" ] && kill "$CHROME_PID" 2>/dev/null || true
    # Give Chrome a moment to flush its profile before we try to remove it.
    sleep 0.5
    rm -rf "$CORAL_HOME" "$CHROME_PROFILE" 2>/dev/null || true
}
trap cleanup EXIT

echo "Starting Coral dev server on :$CORAL_PORT (home=$CORAL_HOME)..."
# Scrub inherited production pointers from the child environment as well.
env -u CORAL_PORT -u CORAL_URL -u CORAL_DATA_DIR -u CORAL_DIR \
    "$CORAL_BIN" --host 127.0.0.1 --port "$CORAL_PORT" --home "$CORAL_HOME" --no-browser \
    > "$CORAL_HOME/server.log" 2>&1 &
CORAL_PID=$!

echo "Starting headless Chrome on CDP port $CDP_PORT..."
"$CHROME" --headless=new --disable-gpu \
    --remote-debugging-port="$CDP_PORT" \
    --user-data-dir="$CHROME_PROFILE" \
    --no-first-run --no-default-browser-check \
    about:blank > "$CHROME_PROFILE/chrome.log" 2>&1 &
CHROME_PID=$!

# Wait for both services to respond.
for i in $(seq 1 30); do
    if curl -sS "http://127.0.0.1:$CORAL_PORT/api/system/status" >/dev/null 2>&1 \
       && curl -sS "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1; then
        break
    fi
    sleep 0.3
done

if ! curl -sS "http://127.0.0.1:$CORAL_PORT/api/system/status" >/dev/null 2>&1; then
    echo "ERROR: Coral server did not come up" >&2
    cat "$CORAL_HOME/server.log"
    exit 2
fi
# Make sure the server that answered is OUR process, not something else.
if ! kill -0 "$CORAL_PID" 2>/dev/null; then
    echo "ERROR: test Coral server exited; another process is answering on :$CORAL_PORT" >&2
    cat "$CORAL_HOME/server.log"
    exit 2
fi
if command -v lsof >/dev/null 2>&1; then
    OWNER="$(lsof -nP -iTCP:"$CORAL_PORT" -sTCP:LISTEN -t 2>/dev/null | sort -u | tr '\n' ' ')"
    if [ "$(echo "$OWNER" | tr -d ' ')" != "$CORAL_PID" ]; then
        echo "ERROR: :$CORAL_PORT listener PID(s) '$OWNER' != test server PID $CORAL_PID" >&2
        exit 2
    fi
fi
echo "Test server PID $CORAL_PID owns :$CORAL_PORT (home=$CORAL_HOME)"
if ! curl -sS "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1; then
    echo "ERROR: Chrome CDP did not come up" >&2
    cat "$CHROME_PROFILE/chrome.log"
    exit 2
fi

export CORAL_URL="http://127.0.0.1:$CORAL_PORT"
export CDP_PORT
cd "$HERE"

# Install deps if needed.
if [ ! -d node_modules ]; then
    echo "Installing test dependencies..."
    npm install --silent
fi

# ONLY=<file> runs a single suite, e.g. ONLY=subagent_tasks.test.js ./run.sh
if [ -n "${ONLY:-}" ]; then
    node "$ONLY"
    exit $?
fi

node acf_model_field.test.js
node terminal_scroll.test.js
node agent_bar_tweaks.test.js
node agent_list_compact.test.js
node agent_state.test.js
node agent_popout.test.js
node subagent_tasks.test.js
node transcript_tools.test.js
node transcript_codex.test.js
