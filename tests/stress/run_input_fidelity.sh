#!/usr/bin/env bash
#
# Input fidelity: what Coral types into an agent's terminal, byte for byte, on each backend.
#
# A mock-agent in raw-input mode (MOCK_AGENT_RAW_DIR) stands in for the agent CLI: its stdin is
# in raw mode with bracketed paste on, as Claude Code and Codex have it, and it records every
# byte its terminal delivers. tests/stress/input_fidelity.py then drives it through Coral's input
# paths (the launch prompt, /send as a person and as automation, /keys, concurrent sends, a task
# notification, a board nudge) with named keys, text that looks like key names, multi-line text,
# 10 KB and 100 KB prompts, non-ASCII and emoji, quotes, backslashes, $(), %VAR%, ^ and Windows
# paths, and compares what arrived with what was sent. It also checks that a prompt opening while an
# automated send is typed never takes that send's Enter (MOCK_AGENT_PROMPT_TRIGGER, audit F4).
#
# Run it once per backend and compare the two results files: any case that differs is a place
# where Windows (pty) and Unix (tmux) agents get different input.
#
#   CORAL_TEST_BACKEND=pty  bash tests/stress/run_input_fidelity.sh   # Windows (Git Bash), or Linux/macOS
#   CORAL_TEST_BACKEND=tmux bash tests/stress/run_input_fidelity.sh   # Linux/macOS (the default)
#   python3 tests/stress/input_fidelity.py compare A.json B.json
#
# Env: CORAL_TEST_BACKEND (tmux|pty, default tmux), CORAL_TEST_PORT (default 8474; 8420 and 8450
# refused), FIDELITY_OUT (results file; default input-fidelity-<os>-<backend>.json in the current
# folder), KEEP_TEST_DATA=1 keeps the temp folder. Requires Go, Python 3 and curl (tmux for tmux).
#
set -euo pipefail
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=${CORAL_TEST_PORT:-8474}
case "$PORT" in 8420|8450) echo "ERROR: refusing port $PORT (a live Coral server); pick another port." >&2; exit 2 ;; esac
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
SERVER_PID=""
OS_NAME="$(uname -s | tr '[:upper:]' '[:lower:]' | sed 's/^mingw.*/windows/; s/^msys.*/windows/')"
FIDELITY_OUT="${FIDELITY_OUT:-$PWD/input-fidelity-$OS_NAME-$CORAL_TEST_BACKEND.json}"

export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOMEMLIMIT="${GOMEMLIMIT:-512MiB}"
export CORAL_TELEMETRY_DISABLED=1
export CORAL_NO_DEMO_SEED=1
export CORAL_NO_WELCOME=1

log() { echo "[input-fidelity] $*"; }

cleanup() {
    local exit_code=$?
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if coral_test_is_windows && [[ -n "${T:-}" ]]; then
        # A killed Windows server leaves its ConPTY children running: stop this run's own
        # mock agents (by their path in this run's folder, never anything else).
        powershell.exe -NoProfile -Command "Get-Process mock-agent -ErrorAction SilentlyContinue | Where-Object { \$_.Path -like '$(cygpath -w "$T")*' } | Stop-Process -Force" >/dev/null 2>&1 || true
    fi
    if [[ $exit_code -ne 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log: ${T:-}"
    else
        rm -rf "${T:-}" 2>/dev/null || true
    fi
    coral_test_sandbox_cleanup
}
trap cleanup EXIT

# ── Setup ────────────────────────────────────────────────────────────

T="$(coral_test_native_path "$(mktemp -d)")"
export TMPDIR="$T"
if coral_test_port_in_use "$PORT"; then
    log "ERROR: Port $PORT is already in use; set CORAL_TEST_PORT to a free port."
    exit 1
fi
export CORAL_DATA_DIR="$T/coral-data"
mkdir -p "$CORAL_DATA_DIR" "$T/raw" "$T/env-dump" "$T/work" "$T/host-home"

log "backend=$CORAL_TEST_BACKEND os=$OS_NAME port=$PORT"
log "Building coral (dev), its agent tools and mock-agent..."
cd "$CORAL_DIR"
go build -p 1 -tags "dev fts5" -o "$T/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync ./cmd/mock-agent

log "Starting coral server on port $PORT..."
coral_test_sandbox   # #4354: stub agent CLIs first on PATH; checked at the end
mapfile -t HOME_ENV < <(coral_test_home_env "$T/host-home")
env -u CORAL_PORT -u CORAL_URL -u CORAL_HOST -u CORAL_DIR -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID \
    -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR -u CORAL_CLOUD_URL \
    "${HOME_ENV[@]}" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
    MOCK_AGENT_RAW_DIR="$T/raw" MOCK_AGENT_ENV_DUMP="$T/env-dump" MOCK_AGENT_IDLE=1 MOCK_AGENT_PROMPT_TRIGGER=OPEN_PROMPT_NOW \
    "$T/coral$CORAL_TEST_EXE" --home "$CORAL_DATA_DIR" --no-browser --cloud-url off --host "$HOST" --port "$PORT" \
    --backend "$CORAL_TEST_BACKEND" >"$T/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 80); do
    command curl -s -m 2 -o /dev/null "$BASE_URL/api/health" && break
    sleep 0.5
done
command curl -s -m 2 -o /dev/null "$BASE_URL/api/health" || { log "ERROR: server did not start"; tail -20 "$T/server.log"; exit 1; }
grep -m1 -E '\[tools\]' "$T/server.log" | sed 's/^/[input-fidelity] server: /' || true

# The dashboard's sign-in: a single-use 'coral open' link traded for the session cookie.
JAR="$T/cookies.txt"
LINK=$("$T/coral$CORAL_TEST_EXE" open --print --url "$BASE_URL" --home "$CORAL_DATA_DIR" | sed -n 's/^ *\(http[^ ]*login_token=[^ ]*\).*/\1/p' | head -1)
[[ -n "$LINK" ]] || { log "ERROR: coral open printed no sign-in link"; exit 1; }
command curl -s -m 10 -o /dev/null -c "$JAR" "$LINK"
COOKIE=$(awk '$6 ~ /^coral_session/ {print $6 "=" $7}' "$JAR" | tail -1)
[[ -n "$COOKIE" ]] || { log "ERROR: the sign-in link gave no session cookie"; exit 1; }

# The mock stands in for the Claude CLI.
command curl -s -m 10 -X PUT "$BASE_URL/api/settings" -H "Content-Type: application/json" \
    -H "Origin: $BASE_URL" -H "Sec-Fetch-Site: same-origin" -H "Cookie: $COOKIE" \
    -d "{\"cli_path_claude\": \"$T/mock-agent$CORAL_TEST_EXE\"}" >/dev/null

# ── Cases ────────────────────────────────────────────────────────────

RC=0
python3 "$SCRIPT_DIR/input_fidelity.py" run --base "$BASE_URL" --data-dir "$CORAL_DATA_DIR" \
    --raw-dir "$T/raw" --work "$T/work" --env-dump "$T/env-dump" --cookie "$COOKIE" \
    --backend "$CORAL_TEST_BACKEND" --out "$(coral_test_native_path "$FIDELITY_OUT")" || RC=$?

coral_test_sandbox_check || RC=1
exit $RC
