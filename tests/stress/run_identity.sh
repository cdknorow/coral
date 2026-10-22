#!/usr/bin/env bash
#
# Identity phase 1 (#3387), end to end on a real, isolated Coral with real agents (a
# stand-in CLI, cmd/mock-agent, that posts through the real coral-board like an agent's
# tool call). Checks:
#   tokens   every launch (incl. 3 team launches x 4 agents), restart and wake mints a
#            token; each agent's environment has CORAL_SESSION_TOKEN; unique; a restart or
#            wake changes it and the old one stops matching
#   leaks    no token in argv (ps), the server log, any file in the data folder (terminal
#            logs, databases), the board, or the tmux server's global environment
#   attribution  coral-board calls from inside agents are attributed to their session with
#            no mismatch, and the agent itself never touches the token
#   impersonation  an agent's token used to post as another subscriber, as Operator, or on
#            another board is detected (counted and logged; phase 1 never refuses)
#   login    the startup sign-in link and `coral open`: single use, cookie exchange
#            (HttpOnly, SameSite=Strict), token stripped by the redirect, reuse refused
#   scale    3 teams x 4 agents plus 2 restart rounds of all 12, within IDENTITY_BUDGET_S
#
# Without --strict the server runs with --strict-local-auth=false: strict local auth is Coral's default since
# #3936, so that mode now checks the opt-out (agents' tokens still attributed, impersonation detected).
# With --strict (or IDENTITY_STRICT=1) the server runs with --strict-local-auth (phase 2, #3418) and the same
# team run must work end to end with ZERO refusals, while impersonation, cross-board, untokened and
# dashboard-without-cookie calls are refused (403 identity_mismatch, 401 session_token_required,
# 401 dashboard_auth_required), the CLI-signed Operator path outside agents works, an agent-marked shell with
# no token never becomes Operator, and the app sign-in path (page load -> /auth -> coral open link) lands on
# the dashboard.
#
# Usage:  tests/stress/run_identity.sh [--strict]   (or: tests/stress/run.sh --identity [--strict])
#   CORAL_IDENTITY_PORT (default 8476), IDENTITY_BUDGET_S (default 240), KEEP_TEST_DATA=1
# Never touches ~/.coral or port 8420; the server runs with CORAL_CLOUD_URL unset and
# --cloud-url off; it and its tmux server are stopped by PID / their own socket.
#
set -euo pipefail
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
STRICT=0
[[ "${1:-}" == "--strict" || "${IDENTITY_STRICT:-0}" == 1 ]] && STRICT=1
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"
# Both backends (#191): the token checks read each agent's own environment from the stand-in's dump, which works the
# same on tmux and on the PTY backend (Windows). Only the two looks into tmux's global environment are tmux's own; on
# the PTY backend there is no shared terminal server holding an environment, so they are skipped, saying so.
NP() { coral_test_native_path "$1"; }   # a path as a Windows program sees it (unchanged elsewhere)
EXE="$CORAL_TEST_EXE"
# process_cmdlines: every process's command line, for the no-token-in-argv check (Git Bash's ps has no -o).
process_cmdlines() {
    if coral_test_is_windows; then
        powershell -NoProfile -Command 'Get-CimInstance Win32_Process | ForEach-Object { $_.CommandLine }' 2>/dev/null
    else
        ps -axo command
    fi
}
# own_mock_pids: this run's stand-ins, by their exact program path, never a pattern.
own_mock_pids() {
    if coral_test_is_windows; then
        local exe; exe="$(cygpath -w "$BIN/mock-agent$EXE")"
        powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='mock-agent.exe'\" | Where-Object { \$_.ExecutablePath -eq '$exe' } | ForEach-Object { \$_.ProcessId }" 2>/dev/null | tr -d '\r'
    else
        ps -eo pid=,args= | awk -v b="$BIN/mock-agent" '$2 == b {print $1}'
    fi
}
# Windows' Python ends its lines with CRLF: a session id read from it would keep the CR and name no dump file. Strip it,
# keeping python's own exit status (some checks below are its sys.exit).
if coral_test_is_windows; then
    python3() { command python3 "$@" | tr -d '\r'; return "${PIPESTATUS[0]}"; }
fi
stop_pid() {
    if coral_test_is_windows; then powershell -NoProfile -Command "Stop-Process -Id $1 -Force -ErrorAction SilentlyContinue" 2>/dev/null; else kill "$1" 2>/dev/null; fi
}
# env -i drops SystemRoot on Windows, and a Go program then can't open a socket: keep the variables Windows itself needs.
WIN_ENV=()
coral_test_is_windows && WIN_ENV=(SystemRoot="${SYSTEMROOT:-${SystemRoot:-C:\Windows}}" WINDIR="${WINDIR:-C:\Windows}")

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CORAL_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/coral-go"
PORT="${CORAL_IDENTITY_PORT:-8476}"
if [[ "$PORT" == 8420 || "$PORT" == 8450 ]]; then echo "ERROR: refusing port $PORT (a live Coral server's port: 8420 production, 8450 the team's); pick another port." >&2; exit 2; fi
BUDGET_S="${IDENTITY_BUDGET_S:-240}"
BASE="http://127.0.0.1:${PORT}"
PASS=0; FAIL=0; SERVER_PID=""
# A short folder: the tmux socket path must stay under the 104-byte limit (macOS).
TMP="$(mktemp -d /tmp/cid.XXXXXX)"
export TMPDIR="$TMP"  # server os.TempDir() droppings land here, removed on exit (#3926)
DATA="$TMP/d"; DUMP="$TMP/dump"; BIN="$TMP/bin"
mkdir -p "$DATA" "$DUMP" "$BIN" "$TMP/h" "$TMP/w"

log()  { echo "[identity] $*"; }
pass() { PASS=$((PASS + 1)); log "PASS: $*"; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; }

cleanup() {
    local code=$?
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    for sock in "$DATA"/tmux.sock "$DATA"/*.sock; do
        [[ -S "$sock" ]] && tmux -S "$sock" kill-server 2>/dev/null || true
    done
    # Stand-ins still running (a timed-out run, #3827): only processes whose program is this run's
    # own mock-agent, by exact path and PID, never a pattern.
    local pid
    for pid in $(own_mock_pids); do
        stop_pid "$pid" || true
    done
    if [[ $code -ne 0 || $FAIL -gt 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log kept: $TMP"
    else
        # A stand-in or tmux still shutting down can write into the folder while it is removed
        # ('Directory not empty'): try again once, and never let it change the run's result.
        rm -rf "$TMP" 2>/dev/null || { sleep 2; rm -rf "$TMP" 2>/dev/null || true; }
    fi
    coral_test_sandbox_cleanup   # #4354
    exit "$code"
}
trap cleanup EXIT

# Strict mode refuses a dashboard-style request without the session cookie: the harness signs in once (below)
# and every call of its own carries the cookie jar. COOKIE_ARGS is empty in phase 1.
JAR="$TMP/jar"; COOKIE_ARGS=""
api() { local m="$1" p="$2"; shift 2; curl -s -m 15 $COOKIE_ARGS -X "$m" "$BASE$p" -H "Content-Type: application/json" "$@"; }
# The identity report is for the dashboard on this computer: send what its page sends.
report() { curl -s -m 10 $COOKIE_ARGS -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" "$BASE/api/system/identity-report"; }
rjson() { report | python3 -c "import sys,json; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1"; }
dumps() { ls "$DUMP"/*.env 2>/dev/null | wc -l | tr -d ' '; }
tokens() { cat "$DUMP"/*.env 2>/dev/null | sed -n 's/^CORAL_SESSION_TOKEN=//p'; }
wait_until() { # wait_until <seconds> <description> <command...>
    local limit=$1 what=$2; shift 2
    local end=$((SECONDS + limit))
    while ! "$@"; do
        if (( SECONDS >= end )); then fail "timed out waiting for $what"; return 1; fi
        sleep 0.5
    done
}
ALL_TOKENS="$TMP/all_tokens"; : > "$ALL_TOKENS"
collect() { tokens >> "$ALL_TOKENS"; sort -u -o "$ALL_TOKENS" "$ALL_TOKENS"; }
# Live team agents (not the blind team): '<name> <session_id>' per line.
sessions() { api GET /api/sessions/live | python3 -c '
import sys,json
d=json.load(sys.stdin); d=d.get("sessions",d) if isinstance(d,dict) else d
for s in d:
    if str(s.get("board_label") or s.get("board_project","")).startswith("id-team-"): print(s["name"], s["session_id"])'; }
# Live team agents whose stand-in has already written its environment dump (with its token): a
# just-restarted, woken or resumed agent has a new session id before its dump exists, and reading a
# missing dump aborted the run (set -e). The stand-in writes the dump asynchronously, so wait for it.
sessions_ready() { local n sid; while read -r n sid; do [[ -n "$sid" ]] && grep -q '^CORAL_SESSION_TOKEN=' "$DUMP/$sid.env" 2>/dev/null && echo "$n $sid"; done < <(sessions); }
wait_sessions_ready() { local end=$((SECONDS + 30)); while [[ -z "$(sessions_ready | head -1)" ]]; do (( SECONDS >= end )) && return 1; sleep 0.5; done; }

log "Building coral, coral-board and the stand-in agent..."
# This build's coral-board and coral-hook-* next to coral: a dev or test server never uses the
# installed app's (#3492), so without them its agents would send no status events.
(cd "$CORAL_DIR" && go build -tags "dev fts5" -o "$BIN/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync \
    && go build -o "$BIN/mock-agent$EXE" ./cmd/mock-agent/)

log "Starting an isolated server on $PORT (data $DATA)..."
# #3421: the server is started from a HOSTILE parent environment, like a test server started
# from an agent's shell: another Coral's port, URL, data folder and an agent identity and
# token. None of it may reach this server's agents (checked below).
HOSTILE_ENV=(CORAL_PORT=1 CORAL_HOST=10.255.255.1 CORAL_URL=http://10.255.255.1:1 CORAL_DIR=/nonexistent/parent-coral
    CORAL_DATA_DIR=/nonexistent/parent-coral CORAL_SESSION_NAME=parent-session-3421 CORAL_SESSION_ID=parent-sid-3421
    CORAL_SUBSCRIBER_ID=ParentRole3421 CORAL_SESSION_TOKEN=leak-token-parent-3421
    # The same names in lower and mixed case: Windows reads environment names without regard to case, so these are the
    # same variables to an agent there, and a filter that strips only CORAL_* would let them through.
    coral_session_token=leak-token-lower-3421 Coral_Dir=/nonexistent/parent-mixed-coral coral_session_name=parent-lower-session-3421)
coral_test_sandbox   # #4354: stub claude/codex/gemini first on PATH; checked at the end
env -u CORAL_CLOUD_URL -u CLAUDE_PROJECTS_DIR HOME="$(NP "$TMP/h")" USERPROFILE="$(NP "$TMP/h")" PATH="$CORAL_TEST_STUB_BIN:$PATH" "${HOSTILE_ENV[@]}" \
    CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 CORAL_TMUX_NO_FALLBACK=1 \
    MOCK_AGENT_CORAL_BOARD="$BIN/coral-board" MOCK_AGENT_ENV_DUMP="$(NP "$DUMP")" MOCK_AGENT_BLIND_PREFIX=id-blind \
    CORAL_TEST_LOGIN_TOKEN_TTL=3s \
    "$BIN/coral" --home "$(NP "$DATA")" --no-browser --cloud-url off --host 127.0.0.1 --port "$PORT" --backend "$CORAL_TEST_BACKEND" \
    $([[ $STRICT == 1 ]] && echo --strict-local-auth || echo --strict-local-auth=false) >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
wait_until 30 "the server" curl -fsS -m 2 "$BASE/api/health" -o /dev/null || exit 1
coral_open() { env -u DISPLAY "$BIN/coral" open --print --url "$BASE" --home "$(NP "$DATA")" | sed -n 's/^ *\(http[^ ]*login_token=[^ ]*\).*/\1/p' | head -1; }
if [[ $STRICT == 1 ]]; then
    log "STRICT: signing the harness in with a 'coral open' link, then every call carries the cookie"
    signin=$(coral_open)
    [[ -n "$signin" ]] && curl -s -m 10 -c "$JAR" -o /dev/null "$signin"
    grep -q coral_session "$JAR" 2>/dev/null && COOKIE_ARGS="-b $JAR" || { fail "could not sign in with a coral open link"; exit 1; }
fi
api PUT /api/settings -d "{\"cli_path_claude\": \"$(NP "$BIN/mock-agent$EXE")\"}" >/dev/null

# ── Dashboard sign-in links ───────────────────────────────────────────
check_link() { # check_link <link> <label>
    local hdr="$TMP/hdr" code loc
    code=$(curl -s -m 10 -o /dev/null -D "$hdr" -w "%{http_code}" "$1")
    loc=$(sed -n 's/^[Ll]ocation: //p' "$hdr" | tr -d '\r')
    if [[ $code == 302 ]] && grep -qi "^set-cookie: coral_session_[0-9a-f]*=.*HttpOnly.*SameSite=Strict" "$hdr" && [[ $loc != *login_token* ]]; then
        pass "$2: one use gives the session cookie (HttpOnly, SameSite=Strict) and a redirect without the token"
    else
        fail "$2: code=$code location=$loc"
    fi
    code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$1")
    [[ $code == 403 ]] && pass "$2: a second use is refused" || fail "$2: reuse answered $code"
}
# Capture the startup token before it is used: the server removes the login_token file after the
# token is consumed (#3628 F7 / #3743), so a later read of the file would fail.
STARTUP_TOKEN="$(tr -d '\n' < "$DATA/login_token")"
check_link "$BASE/?login_token=$STARTUP_TOKEN" "startup link"
open_link=$(coral_open)
[[ -n "$open_link" ]] && check_link "$open_link" "coral open" || fail "coral open printed no link"
# Expiry (this dev build runs with CORAL_TEST_LOGIN_TOKEN_TTL=3s; the real TTL is 10 min).
late_link=$(coral_open); sleep 4
code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$late_link")
[[ $code == 403 ]] && pass "an expired sign-in link is refused" || fail "expired link answered $code"
LOGIN_TOKENS="$STARTUP_TOKEN ${open_link##*=} ${late_link##*=}"

# ── Scale: 3 teams x 4 agents ─────────────────────────────────────────
STARTED=$SECONDS
for t in 1 2 3; do
    out=$(api POST /api/sessions/launch-team -d "{\"board_name\":\"id-team-$t\",\"working_dir\":\"$(NP "$TMP/w")\",\"agents\":[
        {\"agent_type\":\"claude\",\"name\":\"Orchestrator\",\"prompt\":\"Lead.\"},
        {\"agent_type\":\"claude\",\"name\":\"Dev $t\",\"prompt\":\"Dev.\"},
        {\"agent_type\":\"claude\",\"name\":\"QA $t\",\"prompt\":\"QA.\"},
        {\"agent_type\":\"claude\",\"name\":\"Ops $t\",\"prompt\":\"Ops.\"}]}")
    echo "$out" | grep -q '"session' || fail "team $t launch: $out"
done
# A 4th, "blind" team: its stand-ins never read the token (like a real agent).
out=$(api POST /api/sessions/launch-team -d "{\"board_name\":\"id-blind-1\",\"working_dir\":\"$(NP "$TMP/w")\",\"agents\":[
    {\"agent_type\":\"claude\",\"name\":\"Orchestrator\",\"prompt\":\"Lead.\"},{\"agent_type\":\"claude\",\"name\":\"Blind Dev\",\"prompt\":\"Dev.\"},
    {\"agent_type\":\"claude\",\"name\":\"Blind QA\",\"prompt\":\"QA.\"},{\"agent_type\":\"claude\",\"name\":\"Blind Ops\",\"prompt\":\"Ops.\"}]}")
echo "$out" | grep -q '"session' || fail "blind team launch: $out"
wait_until 90 "12 agents to start" bash -c "[[ \$(ls '$DUMP'/*.env 2>/dev/null | wc -l) -ge 12 && \$(ls '$DUMP'/*.blind 2>/dev/null | wc -l) -ge 4 ]]" || true
collect
n=$(tokens | grep -c . || true); u=$(tokens | sort -u | grep -c . || true)
short=$(tokens | awk 'length($0) < 40' | grep -c . || true)
[[ $n -ge 12 && $n -eq $u && $short -eq 0 ]] && pass "12 team agents each got their own token ($u unique, none short)" || fail "tokens after team launch: $n total, $u unique, $short short"

# ── Attribution: coral-board from inside the agents ───────────────────
wait_until 60 "the agents' coral-board posts to be attributed" bash -c "[[ \$(curl -s $COOKIE_ARGS -H 'Origin: $BASE' -H 'Sec-Fetch-Site: same-origin' $BASE/api/system/identity-report | python3 -c 'import sys,json;print(json.load(sys.stdin)[\"callers\"].get(\"agent_token\",0))') -ge 12 ]]" || true
attributed=$(rjson 'd["callers"].get("agent_token",0)')
mism=$(rjson 'sum(d["mismatches"].values())')
[[ $attributed -ge 12 && $mism -eq 0 ]] && pass "coral-board calls from the agents are attributed ($attributed) with no mismatch" || fail "attribution: agent_token=$attributed mismatches=$mism"

# ── No agent action: the agents never handle the token ────────────────
blind_ok=1
wait_until 60 "the blind agents' coral-board posts" bash -c "for f in '$DUMP'/*.blind; do n=\$(head -1 \"\$f\"); curl -s $COOKIE_ARGS -H 'Origin: $BASE' -H 'Sec-Fetch-Site: same-origin' $BASE/api/system/identity-report | python3 -c \"import sys,json;sys.exit(0 if json.load(sys.stdin)['attributed_by_session'].get('\$n',0)>0 else 1)\" || exit 1; done" || blind_ok=0
[[ $blind_ok == 1 && $(rjson 'sum(d["mismatches"].values())') == 0 ]] && pass "4 'blind' agents that never read the token are attributed through coral-board alone, no mismatch" || fail "blind agents not all attributed"
tools=$(rjson 'd["attributed_by_tool"]')
[[ $tools == *coral-board* && $tools != *mock-agent* ]] && pass "only Coral's tools sent the token: $tools" || fail "attributed tools: $tools"
handed_bad=0
for f in "$DUMP"/*.handed; do
    grep -qiE "SESSION_TOKEN|session_tokens" "$f" && { fail "the prompt or settings Coral handed $(basename "$f") mention the token"; handed_bad=1; }
    while read -r tok; do [[ -n "$tok" ]] && grep -qF -- "$tok" "$f" && { fail "a token value in what Coral handed $(basename "$f")"; handed_bad=1; }; done < <(tokens)
done
[[ $handed_bad == 0 ]] && pass "no prompt or settings handed to the $(ls "$DUMP"/*.handed | wc -l | tr -d ' ') agents mentions the token or its file"
[[ -z "$(ls -A "$DATA/session_tokens" 2>/dev/null)" ]] && pass "every token file was read and deleted by the shell before the agent CLI started" || fail "token files left: $(ls "$DATA/session_tokens")"

# ── A hook from inside an agent carries the token ─────────────────────
wait_sessions_ready || log "no live agent has its dump yet"
read -r hname hsid < <(sessions_ready | head -1)
htok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$DUMP/$hsid.env"); hsess=$(sed -n 's/^CORAL_SESSION_NAME=//p' "$DUMP/$hsid.env")
# The agent's own board state (written when it joined) names its board and role.
hstate="$DATA/board_state_$hsess.json"
wait_until 30 "the agent's board state ($hsess)" test -s "$hstate" || true
hsub=$(python3 -c "import sys,json;d=json.load(open(sys.argv[1]));print(d.get('subscriber_id') or d.get('job_title',''))" "$(NP "$hstate")" 2>/dev/null || true)
[[ -n "$hsub" ]] || log "hook check: no subscriber in $hstate"
before=$(rjson 'd["attributed_by_tool"].get("coral-hook-message-check",0)')
hook_out=$(echo '{}' | env -u CORAL_URL CORAL_SESSION_TOKEN="$htok" CORAL_SESSION_NAME="$hsess" CORAL_SUBSCRIBER_ID="$hsub" CORAL_DATA_DIR="$(NP "$DATA")" CORAL_URL="$BASE" "$BIN/coral-hook-message-check" 2>&1 || true)
after=$(rjson 'd["attributed_by_tool"].get("coral-hook-message-check",0)')
[[ $after -gt $before ]] || log "hook check: session=$hsess sub=$hsub output=$hook_out"
[[ $after -gt $before ]] && pass "coral-hook-message-check sends the token from its environment and is attributed" || fail "hook not attributed ($before -> $after)"

# Strict mode: everything above was a normal team run with the harness signed in, so NOTHING was refused.
EXP_TOKEN=0; EXP_MISMATCH=0; EXP_DASH=0; EXP_AGENT_OP=0   # refusals the probes below cause on purpose
if [[ $STRICT == 1 ]]; then
    [[ "$(rjson 'd.get("mode")')" == strict ]] && pass "the identity report says mode strict" || fail "report mode: $(rjson 'd.get("mode")')"
    [[ "$(rjson 'json.dumps(d.get("refused",{}))')" == "{}" ]] && pass "STRICT: a healthy team run (12 agents, attribution, a hook, blind agents) had zero refusals" || fail "STRICT: refusals in a healthy run: $(rjson 'json.dumps(d.get("refused",{}))')"
fi

# ── Restart rounds: new tokens, old ones stop matching ────────────────
OLD=$(tokens | head -1)
for round in 1 2; do
    before=$(dumps)
    while read -r name sid; do
        api POST "/api/sessions/live/$name/restart" -d "{\"session_id\":\"$sid\",\"agent_type\":\"claude\"}" >/dev/null
    done < <(sessions)
    wait_until 90 "round $round restarts" bash -c "[[ \$(ls '$DUMP' | wc -l) -ge $((before + 12)) ]]" || true
    collect
done
# Each stand-in writes its dump asynchronously: give the last ones a moment, then count.
union36() { collect; [[ $(grep -c . "$ALL_TOKENS" || true) -ge 36 ]]; }
# 60 s: on the PTY backend (Windows) the 24 restarts take ~1.8 s each and a restarted stand-in can write its dump after a
# 30 s wait (seen once: 35 of 36, no token reused); only a late dump spends the extra time.
wait_until 60 "all 36 tokens to be reported by their agents" union36 || true
ELAPSED=$((SECONDS - STARTED))
total=$(grep -c . "$ALL_TOKENS" || true)
[[ $total -ge 36 ]] && pass "restarts minted new tokens: $total unique across 3 rounds of 12, no collision" || fail "only $total unique tokens after 2 restart rounds (want 36)"
(( ELAPSED <= BUDGET_S )) && pass "12 agents + 24 restarts in ${ELAPSED}s (budget ${BUDGET_S}s)" || fail "too slow: ${ELAPSED}s > ${BUDGET_S}s"
bad_before=$(rjson 'd["callers"].get("agent_bad_token",0)')
oldcode=$(curl -s -m 10 -o /dev/null -w "%{http_code}" -H "X-Coral-Session-Token: $OLD" -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x")
bad_after=$(rjson 'd["callers"].get("agent_bad_token",0)')
if [[ $STRICT == 1 ]]; then
    EXP_TOKEN=$((EXP_TOKEN + 1))
    [[ $oldcode == 401 ]] && pass "STRICT: a restarted agent's old token is refused (401)" || fail "STRICT: old token answered $oldcode"
else
    [[ $bad_after -eq $((bad_before + 1)) ]] && pass "a restarted agent's old token no longer matches" || fail "old token: bad_token $bad_before -> $bad_after"
fi

# ── Wake: sleep then wake mints a new token for the same session ──────
wait_sessions_ready || log "no live agent has its dump yet"
read -r wname wsid < <(sessions_ready | head -1)
wfile="$DUMP/$wsid.env"; before_tok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$wfile" 2>/dev/null || true)
api POST "/api/sessions/live/$wsid/sleep" -d '{}' >/dev/null; sleep 2
api POST "/api/sessions/live/$wsid/wake" -d '{}' >/dev/null
wait_until 60 "the woken agent's new token" bash -c "[[ -f '$wfile' && \$(sed -n 's/^CORAL_SESSION_TOKEN=//p' '$wfile') != '$before_tok' ]]" && pass "wake minted a new token for $wname" || true
collect

# ── Resume: a new token, the old one stops matching ───────────────────
wait_sessions_ready || log "no live agent has its dump yet"
read -r rname rsid < <(sessions_ready | tail -1)
rfile="$DUMP/$rsid.env"; rold=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$rfile" 2>/dev/null || true)
api POST "/api/sessions/live/$rname/resume" -d "{\"session_id\":\"$rsid\",\"current_session_id\":\"$rsid\",\"agent_type\":\"claude\"}" >/dev/null
if wait_until 60 "the resumed agent's new token" bash -c "[[ -f '$rfile' && \$(sed -n 's/^CORAL_SESSION_TOKEN=//p' '$rfile') != '$rold' ]]"; then
    b0=$(rjson 'd["callers"].get("agent_bad_token",0)')
    rcode=$(curl -s -m 10 -o /dev/null -w "%{http_code}" -H "X-Coral-Session-Token: $rold" -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x")
    b1=$(rjson 'd["callers"].get("agent_bad_token",0)')
    if [[ $STRICT == 1 ]]; then
        EXP_TOKEN=$((EXP_TOKEN + 1))
        [[ $rcode == 401 ]] && pass "STRICT: resume minted a new token and the old one is refused (401)" || fail "STRICT: resume old token answered $rcode"
    else
        [[ $b1 -eq $((b0 + 1)) ]] && pass "resume minted a new token and the old one no longer matches" || fail "resume: old token still matched ($b0 -> $b1)"
    fi
fi
collect

# ── #3421: nothing from the parent's environment reaches an agent ────────
envdumps=$(ls "$DUMP"/*.coralenv 2>/dev/null | wc -l | tr -d ' ')
parent_leaks=$(cat "$DUMP"/*.coralenv 2>/dev/null | grep -cE 'parent-session-3421|parent-sid-3421|ParentRole3421|leak-token-parent-3421|/nonexistent/parent-coral|10\.255\.255\.1|^CORAL_PORT=1$' || true)
# The lower- and mixed-case names: on Windows they ARE the CORAL_* variables (names there ignore case), so none may reach an
# agent; elsewhere names are case-sensitive, they are other variables that no Coral tool reads, and they are only counted.
case_leaks=$(cat "$DUMP"/*.coralenv 2>/dev/null | grep -ciE 'leak-token-lower-3421|parent-mixed-coral|parent-lower-session-3421' || true)
if coral_test_is_windows; then
    parent_leaks=$((parent_leaks + case_leaks))
else
    log "case-sensitive environment: $case_leaks lower/mixed-case parent CORAL names reached agents as other variables (no Coral tool reads them)"
fi
wrong_port=$(cat "$DUMP"/*.coralenv 2>/dev/null | grep '^CORAL_PORT=' | grep -vc "^CORAL_PORT=$PORT$" || true)
tmux_parent=0
if [[ $CORAL_TEST_BACKEND == tmux ]]; then
    tmux_parent=$(tmux -S "$DATA/tmux.sock" show-environment -g 2>/dev/null | grep -cE 'parent-session-3421|ParentRole3421|leak-token-parent-3421|/nonexistent/parent-coral|10\.255\.255\.1' || true)
else
    log "PTY backend: no shared terminal server, so there is no global environment to check (tmux's show-environment -g)"
fi
if [[ $envdumps -ge 12 && $parent_leaks -eq 0 && $wrong_port -eq 0 && $tmux_parent -eq 0 ]]; then
    pass "a server started from a hostile parent env gives its $envdumps agents only its own CORAL_* (port $PORT, its data dir, their own identity)$([[ $CORAL_TEST_BACKEND == tmux ]] && echo "; the tmux server holds none of the parent's")"
else
    fail "parent env reached agents: dumps=$envdumps leaks=$parent_leaks wrong_port=$wrong_port tmux=$tmux_parent"
fi

# ── Leaks: the token is nowhere it shouldn't be ───────────────────────
leaks=0
pslist=$(process_cmdlines)
boards=$(for t in 1 2 3; do api GET "/api/board/id-team-$t/messages/all"; done)
globalenv=""
[[ $CORAL_TEST_BACKEND == tmux ]] && globalenv=$(for s in "$DATA"/tmux.sock "$DATA"/*.sock; do [[ -S "$s" ]] && tmux -S "$s" show-environment -g 2>/dev/null; done || true)
while read -r tok; do
    [[ -z "$tok" ]] && continue
    grep -qF -- "$tok" <<<"$pslist" && { fail "token in a process's argv"; leaks=1; }
    grep -qF -- "$tok" "$TMP/server.log" && { fail "token in the server log"; leaks=1; }
    grep -rqF -D skip -- "$tok" "$DATA" 2>/dev/null && { fail "token in a file in the data folder: $(grep -rlF -D skip -- "$tok" "$DATA" 2>/dev/null | head -1)"; leaks=1; }
    grep -qF -- "$tok" <<<"$boards" && { fail "token on the board"; leaks=1; }
done < "$ALL_TOKENS"
grep -q "CORAL_SESSION_TOKEN=" <<<"$globalenv" && { fail "CORAL_SESSION_TOKEN in the tmux server's global environment"; leaks=1; }
for lt in $LOGIN_TOKENS; do
    grep -qF -- "$lt" "$DATA/coral.log" 2>/dev/null && { fail "a sign-in token in coral.log"; leaks=1; }
done
[[ $leaks -eq 0 ]] && pass "no token in argv, the server log, the data folder (terminal logs, databases), the board$([[ $CORAL_TEST_BACKEND == tmux ]] && echo " or tmux's global env") ($(grep -c . "$ALL_TOKENS") tokens checked)"

# ── Impersonation is detected (phase 1: counted and logged, not refused) ──
wait_sessions_ready || log "no live agent has its dump yet"
read -r iname isid < <(sessions_ready | head -1)
itok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$DUMP/$isid.env")
iboard=$(api GET /api/sessions/live | python3 -c "import sys,json;d=json.load(sys.stdin);d=d.get('sessions',d) if isinstance(d,dict) else d;print([s['board_project'] for s in d if s['session_id']=='$isid'][0])")
# Another team's board, by its id (#3536: boards have ids; their names are labels).
other=$(api GET /api/sessions/live | python3 -c "import sys,json;d=json.load(sys.stdin);d=d.get('sessions',d) if isinstance(d,dict) else d;print(next(s['board_project'] for s in d if str(s.get('board_label') or '').startswith('id-team-') and s['board_project'] != '$iboard'))")
m0=$(rjson 'json.dumps(d["mismatches"])')
post_as() { curl -s -m 10 -o /dev/null -w "%{http_code}" -X POST "$BASE/api/board/$1/messages" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-board/test" -d "{\"subscriber_id\":\"$2\",\"content\":\"impersonation probe\"}"; }
c1=$(post_as "$iboard" "Somebody Else"); c2=$(post_as "$iboard" "Operator")
self=$(api GET /api/sessions/live | python3 -c "import sys,json;d=json.load(sys.stdin);d=d.get('sessions',d) if isinstance(d,dict) else d;print([s.get('display_name') or s['name'] for s in d if s['session_id']=='$isid'][0])")
c3=$(post_as "$other" "$self")
mm=$(rjson 'd["mismatches"]')
if [[ $STRICT == 1 ]]; then
    EXP_MISMATCH=3
    [[ "$c1 $c2 $c3" == "403 403 403" ]] && pass "STRICT: impersonation, Operator and another board are each REFUSED (403: $c1 $c2 $c3)" || fail "STRICT: impersonation probes answered $c1 $c2 $c3 (want 403 403 403)"
fi
python3 - "$m0" "$(rjson 'json.dumps(d["mismatches"])')" <<'PY' && pass "impersonation, Operator and another board each detected once (requests $([[ $STRICT == 1 ]] && echo refused || echo not refused): $c1 $c2 $c3)" || fail "mismatch counts: before $m0 after $mm"
import sys, json
a, b = json.loads(sys.argv[1]), json.loads(sys.argv[2])
d = {k: b.get(k, 0) - a.get(k, 0) for k in ("impersonation", "operator", "cross_board")}
sys.exit(0 if d == {"impersonation": 1, "operator": 1, "cross_board": 1} else 1)
PY
grep -q "\[identity\] mismatch" "$TMP/server.log" && pass "the server log has the mismatch lines" || fail "no mismatch line in the server log"

# ── STRICT MODE (phase 2, #3418) ──────────────────────────────────────
if [[ $STRICT == 1 ]]; then
    code_of() { curl -s -m 10 -o /dev/null -w "%{http_code}" "$@"; }
    body_of() { curl -s -m 10 "$@"; }

    # The refusal body names its code.
    body=$(body_of -X POST "$BASE/api/board/$iboard/messages" $COOKIE_ARGS -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-board/test" -d '{"subscriber_id":"Somebody Else","content":"x"}')
    EXP_MISMATCH=4
    grep -q identity_mismatch <<<"$body" && pass "STRICT: the refusal says identity_mismatch" || fail "STRICT: refusal body: $body"

    # Untokened agent-tool call: refused, never served.
    c=$(code_of -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x"); EXP_TOKEN=$((EXP_TOKEN + 1))
    b=$(body_of -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x"); EXP_TOKEN=$((EXP_TOKEN + 1))
    [[ $c == 401 ]] && grep -q session_token_required <<<"$b" && pass "STRICT: an agent-tool call with no token is refused (401 session_token_required)" || fail "STRICT: untokened call answered $c $b"
    # ...and the real coral-board from an agent-marked shell whose token was removed: refused, never Operator.
    st0=$(rjson 'd.get("refused",{}).get("session_token_required",0)')
    out=$(env -i "${WIN_ENV[@]}" PATH="$PATH" HOME="$(NP "$TMP/h")" USERPROFILE="$(NP "$TMP/h")" CORAL_DATA_DIR="$(NP "$DATA")" CORAL_URL="$BASE" CORAL_SESSION_NAME="stripped-agent" CORAL_SUBSCRIBER_ID="Operator" "$BIN/coral-board" post "stripped token probe" 2>&1) && rc=0 || rc=$?
    EXP_TOKEN=$((EXP_TOKEN + 1))
    st1=$(rjson 'd.get("refused",{}).get("session_token_required",0)')
    # The SERVER must have refused it (the counter moved), not merely coral-board failing for another reason.
    if [[ $rc -ne 0 && $st1 -gt $st0 ]] && ! api GET "/api/board/id-team-1/messages/all" | grep -q "stripped token probe"; then
        pass "STRICT: an agent-marked shell with its token removed gets nothing posted (never Operator): ${out:0:80}"
    else
        fail "STRICT: stripped-token post rc=$rc refused $st0 -> $st1 out=$out"
    fi

    # A human at a normal shell (no agent markers) signs with the CLI token and acts as the Operator.
    out=$(env -i "${WIN_ENV[@]}" PATH="$PATH" HOME="$(NP "$TMP/h")" USERPROFILE="$(NP "$TMP/h")" CORAL_DATA_DIR="$(NP "$DATA")" CORAL_URL="$BASE" "$BIN/coral-board" join id-team-1 --as Operator 2>&1) && rc=0 || rc=$?
    if [[ $rc -eq 0 ]]; then
        out2=$(env -i "${WIN_ENV[@]}" PATH="$PATH" HOME="$(NP "$TMP/h")" USERPROFILE="$(NP "$TMP/h")" CORAL_DATA_DIR="$(NP "$DATA")" CORAL_URL="$BASE" "$BIN/coral-board" post "operator from a plain shell" 2>&1) && rc2=0 || rc2=$?
        if [[ $rc2 -eq 0 ]] && api GET "/api/board/id-team-1/messages/all" | grep -q "operator from a plain shell"; then
            pass "STRICT: coral-board from a plain shell signs with the CLI token and posts as the Operator"
        else
            fail "STRICT: plain-shell post rc=$rc2 $out2"
        fi
    else
        fail "STRICT: plain-shell join as Operator failed: $out"
    fi

    # The dashboard without the session cookie.
    c=$(code_of "$BASE/api/sessions/live"); EXP_DASH=$((EXP_DASH + 1))
    b=$(body_of "$BASE/api/sessions/live"); EXP_DASH=$((EXP_DASH + 1))
    [[ $c == 401 ]] && grep -q dashboard_auth_required <<<"$b" && pass "STRICT: /api without the cookie: 401 dashboard_auth_required" || fail "STRICT: /api without a cookie answered $c"
    c=$(code_of -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" "$BASE/api/system/identity-report"); EXP_DASH=$((EXP_DASH + 1))
    [[ $c == 401 ]] && pass "STRICT: the identity report is cookie-gated too (401 without the cookie)" || fail "STRICT: report without cookie answered $c"
    c=$(code_of "$BASE/api/health"); [[ $c == 200 ]] && pass "STRICT: /api/health stays open" || fail "STRICT: health answered $c"
    c=$(code_of -X POST "$BASE/api/cloud/cli-hello" -H "Content-Type: application/json" -d '{"nonce":"x"}'); [[ $c != 401 ]] && pass "STRICT: the CLI hello stays open ($c)" || fail "STRICT: cli-hello answered 401"
    hdr="$TMP/hdr2"; c=$(curl -s -m 10 -o /dev/null -D "$hdr" -w "%{http_code}" "$BASE/"); loc=$(sed -n 's/^[Ll]ocation: //p' "$hdr" | tr -d '\r')
    [[ $c == 307 && $loc == *"/auth?locked=1"* ]] && pass "STRICT: a page load without the cookie goes to the locked /auth ($loc)" || fail "STRICT: / without a cookie: $c $loc"
    # /auth is the one sign-in page since #3948 (a password form, "Use an API key instead" behind a click), not a
    # separate locked page: in strict mode it is marked strict and never carries the API key itself.
    page=$(body_of "$BASE/auth?locked=1")
    apikey=""; [[ -s "$DATA/api_key" ]] && apikey=$(tr -d '\n' < "$DATA/api_key")
    if grep -q 'data-strict="true"' <<<"$page" && { [[ -z "$apikey" ]] || ! grep -qF -- "$apikey" <<<"$page"; }; then pass "STRICT: /auth is the sign-in page, marked strict, and does not show the API key"; else fail "STRICT: the /auth page: ${page:0:120}"; fi

    # The app/tray sign-in path: cookie-less page load -> /auth -> a fresh link from the CLI handshake -> the dashboard.
    link=$(coral_open); app_jar="$TMP/appjar"; rm -f "$app_jar"
    c=$(curl -s -m 10 -c "$app_jar" -o /dev/null -w "%{http_code}" "$link")
    c2=$(curl -s -m 10 -b "$app_jar" -o "$TMP/dash.html" -w "%{http_code}" "$BASE/")
    if [[ $c == 302 && $c2 == 200 ]] && grep -qi "<html" "$TMP/dash.html"; then pass "STRICT: the app sign-in path (coral open link) lands on the dashboard"; else fail "STRICT: sign-in $c then / $c2"; fi
    c=$(code_of -b "$app_jar" "$link"); [[ $c == 403 ]] && pass "STRICT: the used sign-in link cannot be replayed (403)" || fail "STRICT: replay answered $c"

    # #3424: an agent's token can't start code on this computer (task runs, workflows, jobs).
    c=$(code_of -X POST "$BASE/api/tasks/run" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{"prompt":"x","repo_path":"/tmp"}')
    c2=$(code_of -X POST "$BASE/api/workflows" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{"name":"x","steps":[{"name":"s","type":"shell","command":"true"}]}')
    EXP_AGENT_OP=2
    [[ $c == 403 && $c2 == 403 ]] && pass "STRICT: an agent's token can't start a task run or create a workflow (403 forbidden_for_agent)" || fail "STRICT: agent task run answered $c, workflow $c2"

    # #3445: nor change Coral's settings (cli_path_* is persistent code execution) or stop the Operator's runs;
    # launching agents stays allowed (a team launches its agents) and is recorded.
    c3=$(code_of -X PUT "$BASE/api/settings" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{"cli_path_claude":"/bin/sh"}')
    c4=$(code_of -X POST "$BASE/api/workflows/runs/1/kill" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{}')
    c5=$(code_of -X POST "$BASE/api/tasks/runs/1/kill" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{}')
    EXP_AGENT_OP=$((EXP_AGENT_OP + 3))
    [[ $c3 == 403 && $c4 == 403 && $c5 == 403 ]] && pass "STRICT: an agent's token can't change Coral's settings or stop a workflow or task run (403 forbidden_for_agent)" || fail "STRICT: agent settings PUT answered $c3, workflow kill $c4, task kill $c5"
    [[ "$(api GET /api/settings | python3 -c 'import sys,json;print(json.load(sys.stdin).get("cli_path_claude",""))' 2>/dev/null)" != "/bin/sh" ]] && pass "STRICT: the refused settings change did not happen (cli_path_claude is not /bin/sh)" || fail "STRICT: cli_path_claude was changed by an agent"
    # #3445 (QA finding): the dashboard-provenance headers (Origin, Sec-Fetch-Site) are forgeable, so an agent token
    # plus those two headers must not mint a sign-in link (that made the agent the Operator) or rotate the API key.
    # (Webhooks were removed in #3646, so there is no webhook route to probe.)
    c7=$(code_of -X POST "$BASE/api/system/login-token" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" -A "coral-agent/test" -d '{}')
    c8=$(code_of -X POST "$BASE/api/system/api-key/regenerate" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" -A "coral-agent/test" -d '{}')
    EXP_AGENT_OP=$((EXP_AGENT_OP + 2))
    [[ $c7 == 403 && $c8 == 403 ]] && pass "STRICT: an agent token (even with forged Origin and Sec-Fetch-Site) can't mint a sign-in link or rotate the API key" || fail "STRICT: agent login-token $c7, key regenerate $c8"
    c6=$(code_of -X POST "$BASE/api/sessions/launch" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d '{}')
    [[ $c6 != 403 && $c6 != 401 ]] && pass "STRICT: an agent can still launch agents (a bad request is $c6, not a refusal)" || fail "STRICT: an agent launch was refused ($c6)"
    rj=$(report); grep -q "POST /api/sessions/launch" <<<"$rj" && pass "STRICT: the agent's launch is recorded in agent_operator_calls" || fail "STRICT: the agent launch is not in agent_operator_calls"

    # Refusals are exactly the deliberate probes (by reason), none anywhere else.
    refused_now=$(rjson 'json.dumps(d.get("refused",{}))')
    if python3 - "$refused_now" "$EXP_MISMATCH" "$EXP_TOKEN" "$EXP_DASH" "$EXP_AGENT_OP" <<'CHECKEOF'
import sys, json
got = json.loads(sys.argv[1]); want = {"identity_mismatch": int(sys.argv[2]), "session_token_required": int(sys.argv[3]), "dashboard_auth_required": int(sys.argv[4]), "forbidden_for_agent": int(sys.argv[5])}
sys.exit(0 if all(got.get(k, 0) == v for k, v in want.items()) and set(got) <= set(want) else 1)
CHECKEOF
    then pass "STRICT: the report's refused counts are exactly the deliberate probes ($refused_now)"; else fail "STRICT: refused $refused_now vs expected mismatch=$EXP_MISMATCH token=$EXP_TOKEN dash=$EXP_DASH"; fi
fi


# ── #3445, default mode: an agent's settings change, run kill and launch go through and are RECORDED ──
if [[ $STRICT != 1 ]]; then
    for req in "PUT /api/settings {}" "POST /api/workflows/runs/1/kill {}" "POST /api/tasks/runs/1/kill {}" "POST /api/sessions/launch {}"; do
        set -- $req
        curl -s -m 10 -o /dev/null -X "$1" "$BASE$2" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-agent/test" -d "$3"
    done
    rj=$(report); miss=""
    for route in "PUT /api/settings" "POST /api/workflows/runs/{runID}/kill" "POST /api/tasks/runs/{runID}/kill" "POST /api/sessions/launch"; do grep -qF "$route" <<<"$rj" || miss="$miss [$route]"; done
    [[ -z "$miss" ]] && pass "an agent's settings change, run kills and launch are recorded in agent_operator_calls (log only in default mode)" || fail "not recorded in agent_operator_calls:$miss"
fi

log "What strict mode would refuse, by caller, tool and route (#3418; this script's own curl calls are local_other curl):"
report | python3 -c '
import sys, json
for k, v in sorted(json.load(sys.stdin).get("would_break_by_route", {}).items(), key=lambda kv: -kv[1]):
    print("[identity]   %5d  %s" % (v, k))'
coral_test_sandbox_check || FAIL=$((FAIL + 1))   # #4354
log "Done: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
