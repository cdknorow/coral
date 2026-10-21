#!/usr/bin/env bash
#
# Folder "Kill all" must kill only that folder group's agents (#3053, #3058).
#
# Real agents (mock-agent through the tmux backend) on an isolated server, in ONE
# working directory: 2 solo agents, a team of 2 launched with the real team
# launch, and 1 archived agent. The real dashboard (headless Chrome) clicks the
# folder group's Kill all and confirms; then the API and tmux must show exactly
# the 2 solo agents gone and the team + archived agents untouched. Run twice:
# Group by Team on, then off (fresh solo agents for the second round).
#
# Usage: bash tests/stress/run_kill_all.sh        (or: bash tests/stress/run.sh --kill-all)
# Env:   CORAL_TEST_PORT (default 8477; 8420 refused), CORAL_SRC_DIR (coral-go tree to build;
#        default this repo's, point it at a pre-fix checkout to confirm the test fails there),
#        CORAL_CHROME_BIN, KEEP_TEST_DATA=1.
# Needs: go, tmux, node + tests/frontend dependencies, Chrome/Chromium. The tmux server is the
# test's own socket under its CORAL_DATA_DIR (CORAL_TMUX_NO_FALLBACK=1), never your default one.
#
set -euo pipefail
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"
coral_test_backend_only tmux "its checks read the tmux socket (has-session) to see which agents a kill-all ended"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_SRC="${CORAL_SRC_DIR:-$REPO_ROOT/coral-go}"
PORT=${CORAL_TEST_PORT:-8477}
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
PASS=0
FAIL=0
SERVER_PID=""

log()  { echo "[kill-all] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }
api()  { local m="$1" p="$2"; shift 2; curl -s -m 15 -X "$m" "${BASE_URL}${p}" -H "Content-Type: application/json" "$@"; }

# Short temp path: the tmux socket lives under it and Unix socket paths are limited (~104 bytes).
TMP="$(mktemp -d /tmp/coral-killall.XXXXXX)"
export TMPDIR="$TMP"  # (#3926)
export CORAL_DATA_DIR="$TMP/data"
export CORAL_NO_DEMO_SEED=1  # no demo workflow/job (it would run a real agent)
export CORAL_NO_WELCOME=1    # no first-launch screen (#3221)
export CORAL_TMUX_NO_FALLBACK=1
SOCK="$CORAL_DATA_DIR/tmux.sock"
WORK="$TMP/proj"
mkdir -p "$CORAL_DATA_DIR" "$WORK"

cleanup() {
    local code=$?
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    tmux -S "$SOCK" kill-server 2>/dev/null || true
    if [[ $code -ne 0 || $FAIL -gt 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log kept: $TMP"
    else
        rm -rf "$TMP"
    fi
    coral_test_sandbox_cleanup   # #4354
}
trap cleanup EXIT

if lsof -iTCP:"$PORT" -sTCP:LISTEN -t >/dev/null 2>&1; then
    log "ERROR: port $PORT is in use; set CORAL_TEST_PORT to a free port."; exit 1
fi
for bin in go tmux curl python3; do command -v "$bin" >/dev/null || { log "ERROR: $bin is required"; exit 1; }; done
source "$SCRIPT_DIR/../lib/find_node.sh"; coral_find_node || { log "ERROR: no Node.js found"; exit 1; }

log "Building coral (dev) and mock-agent from $CORAL_SRC ..."
(cd "$CORAL_SRC" && go build -tags "dev fts5" -o "$TMP/coral" ./cmd/coral/ && go build -o "$TMP/mock-agent" ./cmd/mock-agent/)

log "Starting coral on $BASE_URL (data $CORAL_DATA_DIR) ..."
# The server gets its own HOME and no host-data dirs, so it never indexes this machine's real
# ~/.claude/projects, ~/.codex, ~/.gemini or ~/.pi (privacy and deterministic results).
mkdir -p "$TMP/host-home"
coral_test_sandbox   # #4354: stub claude/codex/gemini first on PATH; checked at the end
env -u CORAL_PORT -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u TMUX -u TMUX_PANE -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR HOME="$TMP/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
    "$TMP/coral" --home "$CORAL_DATA_DIR" --no-browser --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
# Ready only when OUR server says it bound the port (its own log line), not when anything answers.
for _ in $(seq 1 60); do
    grep -q "Coral dashboard: http://localhost:${PORT}" "$TMP/server.log" 2>/dev/null && break
    kill -0 "$SERVER_PID" 2>/dev/null || { log "ERROR: server exited:"; tail -20 "$TMP/server.log"; exit 1; }
    sleep 0.5
done
grep -q "Coral dashboard: http://localhost:${PORT}" "$TMP/server.log" || { log "ERROR: server did not start"; exit 1; }
curl -s -m 2 "$BASE_URL/api/health" >/dev/null || { log "ERROR: $BASE_URL does not answer"; exit 1; }
api PUT "/api/settings" -d "{\"cli_path_claude\": \"$TMP/mock-agent\"}" >/dev/null

# Rows of the live list as TSV: session_id, name, display_name, board_project, sleeping, archived, tmux_session
live() {
    api GET "/api/sessions/live" | python3 -c '
import json, sys
d = json.load(sys.stdin)
d = d if isinstance(d, list) else d.get("sessions", [])
for s in d:
    print("\t".join(str(x) for x in (s.get("session_id"), s.get("name"), s.get("display_name"), s.get("board_project") or "-",
                                     int(bool(s.get("sleeping"))), int(bool(s.get("archived"))), s.get("tmux_session") or "-")))'
}
sid_of()  { live | awk -F'\t' -v n="$1" '$3 == n {print $1; exit}'; }
row_of()  { live | awk -F'\t' -v s="$1" '$1 == s'; }
tmux_has() { tmux -S "$SOCK" has-session -t "$1" 2>/dev/null; }

launch_solo() {
    api POST "/api/sessions/launch" -d "{\"working_dir\": \"$WORK\", \"agent_type\": \"claude\", \"display_name\": \"$1\", \"backend\": \"tmux\"}" >/dev/null
}
wait_running() {  # display names...
    for _ in $(seq 1 60); do
        local ok=1
        for n in "$@"; do
            local sid; sid=$(sid_of "$n")
            [[ -n "$sid" ]] || { ok=0; break; }
            local tm; tm=$(row_of "$sid" | cut -f7)
            [[ "$tm" != "-" ]] && tmux_has "$tm" || { ok=0; break; }
        done
        [[ $ok == 1 ]] && return 0
        sleep 0.5
    done
    log "ERROR: agents did not start: $*"; live | sed 's/^/[kill-all]   /'; tail -20 "$TMP/server.log"; exit 1
}

log "Launching 2 solo agents, a team of 2 and an archived agent in $WORK ..."
launch_solo "Solo A"; launch_solo "Solo B"; launch_solo "Archive Me"
team_launch=$(api POST "/api/sessions/launch-team" -d "{
    \"board_name\": \"killall-team\", \"working_dir\": \"$WORK\", \"backend\": \"tmux\",
    \"agents\": [
        {\"agent_type\": \"claude\", \"name\": \"Team Lead\", \"role\": \"orchestrator\", \"prompt\": \"Coordinate.\"},
        {\"agent_type\": \"claude\", \"name\": \"Team Dev\", \"prompt\": \"Implement.\"}
    ]}")
# A launch starts a board with its own id (#3536: b-...), which the live list's board_project shows; "killall-team" is its label.
TEAM_BOARD=$(echo "$team_launch" | python3 -c "import sys,json; print(json.load(sys.stdin).get('board_id',''))" 2>/dev/null || echo "")
[[ -n "$TEAM_BOARD" ]] || { log "ERROR: the team launch returned no board_id: $team_launch"; exit 1; }
wait_running "Solo A" "Solo B" "Archive Me"
TEAM_SIDS=$(live | awk -F'\t' -v b="$TEAM_BOARD" '$4 == b {print $1}')
[[ $(echo "$TEAM_SIDS" | grep -c .) == 2 ]] || { log "ERROR: expected 2 team agents"; live; exit 1; }
for t in $TEAM_SIDS; do tmux_has "$(row_of "$t" | cut -f7)" || { log "ERROR: team agent $t has no tmux session"; exit 1; }; done
ARCH=$(sid_of "Archive Me")
api POST "/api/sessions/live/$ARCH/archive" >/dev/null
sleep 1
[[ $(row_of "$ARCH" | cut -f6) == 1 ]] || { log "ERROR: archive did not take"; exit 1; }

# The bug needs every agent in ONE folder group: the folder key is the agent name.
FOLDER=$(row_of "$(sid_of "Solo A")" | cut -f2)
names=$(live | cut -f2 | sort -u)
if [[ "$names" != "$FOLDER" ]]; then
    log "ERROR: precondition: all agents must share one folder (agent name); got: $(echo $names)"; exit 1
fi
pass "precondition: 2 solo, 2 team, 1 archived agent all in folder '$FOLDER'"

kill_round() {  # label, group-by-team on|off
    local label="$1" grouping="$2"
    local a b; a=$(sid_of "Solo A"); b=$(sid_of "Solo B")
    local ta tb; ta=$(row_of "$a" | cut -f7); tb=$(row_of "$b" | cut -f7)
    local team_tmux=(); for t in $TEAM_SIDS; do team_tmux+=("$(row_of "$t" | cut -f7)"); done
    local ids; ids=$(echo "$a,$b,$ARCH,$(echo $TEAM_SIDS | tr ' ' ',')")
    log "[$label] Kill all on folder '$FOLDER' in the dashboard (Group by Team $grouping) ..."
    local out
    # Strict local auth is the default (#3936): the tab signs in with a single-use 'coral open' link.
    local link; link=$("$TMP/coral" open --print --url "$BASE_URL" --home "$CORAL_DATA_DIR" | sed -n 's/^ *\(http[^ ]*login_token=[^ ]*\).*/\1/p' | head -1)
    [[ -n "$link" ]] || { fail "[$label] coral open printed no sign-in link"; return; }
    if ! out=$("$NODE" "$SCRIPT_DIR/kill_all_browser.js" "$BASE_URL" "$FOLDER" "$grouping" "$ids" "$link"); then
        fail "[$label] dashboard Kill all did not run"; return
    fi
    log "[$label] confirm: $(echo "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["confirm"])')"
    if echo "$out" | grep -q 'Kill all 2 agent'; then pass "[$label] confirm counts the 2 solo agents"; else fail "[$label] confirm text: $out"; fi
    sleep 2
    for s in "$a" "$b"; do
        if [[ -z "$(row_of "$s")" ]]; then pass "[$label] solo $s gone from the API"; else fail "[$label] solo $s still listed: $(row_of "$s")"; fi
    done
    for t in "$ta" "$tb"; do
        if tmux_has "$t"; then fail "[$label] solo tmux session $t still running"; else pass "[$label] solo tmux session $t gone"; fi
    done
    for t in $TEAM_SIDS; do
        local r; r=$(row_of "$t")
        if [[ -n "$r" && $(echo "$r" | cut -f5) == 0 ]]; then pass "[$label] team agent $t untouched (listed, awake)"; else fail "[$label] team agent $t killed or asleep: '${r}'"; fi
    done
    for tm in "${team_tmux[@]}"; do
        if tmux_has "$tm"; then pass "[$label] team tmux session $tm still running"; else fail "[$label] team tmux session $tm was killed"; fi
    done
    local r; r=$(row_of "$ARCH")
    if [[ -n "$r" && $(echo "$r" | cut -f6) == 1 ]]; then pass "[$label] archived agent untouched"; else fail "[$label] archived agent killed or changed: '${r}'"; fi
}

kill_round "group-by-team on" on

log "Relaunching 2 solo agents for the second round ..."
launch_solo "Solo A"; launch_solo "Solo B"
wait_running "Solo A" "Solo B"
kill_round "group-by-team off" off

coral_test_sandbox_check || FAIL=$((FAIL + 1))   # #4354
log "Result: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
