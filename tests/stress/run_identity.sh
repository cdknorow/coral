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
# Usage:  tests/stress/run_identity.sh          (or: tests/stress/run.sh --identity)
#   CORAL_IDENTITY_PORT (default 8476), IDENTITY_BUDGET_S (default 240), KEEP_TEST_DATA=1
# Never touches ~/.coral or port 8420; the server runs with CORAL_CLOUD_URL unset and
# --cloud-url off; it and its tmux server are stopped by PID / their own socket.
#
set -euo pipefail
# Clears an agent shell's identity and production links (and documents: never run bare 'tmux' here).
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CORAL_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/coral-go"
PORT="${CORAL_IDENTITY_PORT:-8476}"
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
BUDGET_S="${IDENTITY_BUDGET_S:-240}"
BASE="http://127.0.0.1:${PORT}"
PASS=0; FAIL=0; SERVER_PID=""
# A short folder: the tmux socket path must stay under the 104-byte limit (macOS).
TMP="$(mktemp -d /tmp/cid.XXXXXX)"
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
    if [[ $code -ne 0 || $FAIL -gt 0 || ${KEEP_TEST_DATA:-0} == 1 ]]; then
        log "Test data and server log kept: $TMP"
    else
        rm -rf "$TMP"
    fi
}
trap cleanup EXIT

api() { local m="$1" p="$2"; shift 2; curl -s -m 15 -X "$m" "$BASE$p" -H "Content-Type: application/json" "$@"; }
# The identity report is for the dashboard on this computer: send what its page sends.
report() { curl -s -m 10 -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" "$BASE/api/system/identity-report"; }
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
    if str(s.get("board_project","")).startswith("id-team-"): print(s["name"], s["session_id"])'; }

log "Building coral, coral-board and the stand-in agent..."
(cd "$CORAL_DIR" && go build -tags "dev fts5" -o "$BIN/coral" ./cmd/coral/ && go build -o "$BIN/coral-board" ./cmd/coral-board/ \
    && go build -o "$BIN/mock-agent" ./cmd/mock-agent/ && go build -o "$BIN/coral-hook-message-check" ./cmd/coral-hook-message-check/)

log "Starting an isolated server on $PORT (data $DATA)..."
env -u CORAL_CLOUD_URL -u CORAL_PORT -u CORAL_URL -u CLAUDE_PROJECTS_DIR HOME="$TMP/h" \
    CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 CORAL_TMUX_NO_FALLBACK=1 \
    MOCK_AGENT_CORAL_BOARD="$BIN/coral-board" MOCK_AGENT_ENV_DUMP="$DUMP" MOCK_AGENT_BLIND_PREFIX=id-blind \
    CORAL_TEST_LOGIN_TOKEN_TTL=3s \
    "$BIN/coral" --home "$DATA" --no-browser --cloud-url off --host 127.0.0.1 --port "$PORT" --backend tmux >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
wait_until 30 "the server" curl -fsS -m 2 "$BASE/api/health" -o /dev/null || exit 1
api PUT /api/settings -d "{\"cli_path_claude\": \"$BIN/mock-agent\"}" >/dev/null

# ── Dashboard sign-in links ───────────────────────────────────────────
check_link() { # check_link <link> <label>
    local hdr="$TMP/hdr" code loc
    code=$(curl -s -m 10 -o /dev/null -D "$hdr" -w "%{http_code}" "$1")
    loc=$(sed -n 's/^[Ll]ocation: //p' "$hdr" | tr -d '\r')
    if [[ $code == 302 ]] && grep -qi "^set-cookie: coral_session=.*HttpOnly.*SameSite=Strict" "$hdr" && [[ $loc != *login_token* ]]; then
        pass "$2: one use gives the session cookie (HttpOnly, SameSite=Strict) and a redirect without the token"
    else
        fail "$2: code=$code location=$loc"
    fi
    code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$1")
    [[ $code == 403 ]] && pass "$2: a second use is refused" || fail "$2: reuse answered $code"
}
check_link "$BASE/?login_token=$(tr -d '\n' < "$DATA/login_token")" "startup link"
coral_open() { env -u DISPLAY "$BIN/coral" open --print --url "$BASE" --home "$DATA" | sed -n 's/^ *\(http[^ ]*login_token=[^ ]*\).*/\1/p' | head -1; }
open_link=$(coral_open)
[[ -n "$open_link" ]] && check_link "$open_link" "coral open" || fail "coral open printed no link"
# Expiry (this dev build runs with CORAL_TEST_LOGIN_TOKEN_TTL=3s; the real TTL is 10 min).
late_link=$(coral_open); sleep 4
code=$(curl -s -m 10 -o /dev/null -w "%{http_code}" "$late_link")
[[ $code == 403 ]] && pass "an expired sign-in link is refused" || fail "expired link answered $code"
LOGIN_TOKENS="$(tr -d '\n' < "$DATA/login_token") ${open_link##*=} ${late_link##*=}"

# ── Scale: 3 teams x 4 agents ─────────────────────────────────────────
STARTED=$SECONDS
for t in 1 2 3; do
    out=$(api POST /api/sessions/launch-team -d "{\"board_name\":\"id-team-$t\",\"working_dir\":\"$TMP/w\",\"agents\":[
        {\"agent_type\":\"claude\",\"name\":\"Orchestrator\",\"prompt\":\"Lead.\"},
        {\"agent_type\":\"claude\",\"name\":\"Dev $t\",\"prompt\":\"Dev.\"},
        {\"agent_type\":\"claude\",\"name\":\"QA $t\",\"prompt\":\"QA.\"},
        {\"agent_type\":\"claude\",\"name\":\"Ops $t\",\"prompt\":\"Ops.\"}]}")
    echo "$out" | grep -q '"session' || fail "team $t launch: $out"
done
# A 4th, "blind" team: its stand-ins never read the token (like a real agent).
out=$(api POST /api/sessions/launch-team -d "{\"board_name\":\"id-blind-1\",\"working_dir\":\"$TMP/w\",\"agents\":[
    {\"agent_type\":\"claude\",\"name\":\"Orchestrator\",\"prompt\":\"Lead.\"},{\"agent_type\":\"claude\",\"name\":\"Blind Dev\",\"prompt\":\"Dev.\"},
    {\"agent_type\":\"claude\",\"name\":\"Blind QA\",\"prompt\":\"QA.\"},{\"agent_type\":\"claude\",\"name\":\"Blind Ops\",\"prompt\":\"Ops.\"}]}")
echo "$out" | grep -q '"session' || fail "blind team launch: $out"
wait_until 90 "12 agents to start" bash -c "[[ \$(ls '$DUMP'/*.env 2>/dev/null | wc -l) -ge 12 && \$(ls '$DUMP'/*.blind 2>/dev/null | wc -l) -ge 4 ]]" || true
collect
n=$(tokens | grep -c . || true); u=$(tokens | sort -u | grep -c . || true)
short=$(tokens | awk 'length($0) < 40' | grep -c . || true)
[[ $n -ge 12 && $n -eq $u && $short -eq 0 ]] && pass "12 team agents each got their own token ($u unique, none short)" || fail "tokens after team launch: $n total, $u unique, $short short"

# ── Attribution: coral-board from inside the agents ───────────────────
wait_until 60 "the agents' coral-board posts to be attributed" bash -c "[[ \$(curl -s -H 'Origin: $BASE' -H 'Sec-Fetch-Site: same-origin' $BASE/api/system/identity-report | python3 -c 'import sys,json;print(json.load(sys.stdin)[\"callers\"].get(\"agent_token\",0))') -ge 12 ]]" || true
attributed=$(rjson 'd["callers"].get("agent_token",0)')
mism=$(rjson 'sum(d["mismatches"].values())')
[[ $attributed -ge 12 && $mism -eq 0 ]] && pass "coral-board calls from the agents are attributed ($attributed) with no mismatch" || fail "attribution: agent_token=$attributed mismatches=$mism"

# ── No agent action: the agents never handle the token ────────────────
blind_ok=1
wait_until 60 "the blind agents' coral-board posts" bash -c "for f in '$DUMP'/*.blind; do n=\$(head -1 \"\$f\"); curl -s -H 'Origin: $BASE' -H 'Sec-Fetch-Site: same-origin' $BASE/api/system/identity-report | python3 -c \"import sys,json;sys.exit(0 if json.load(sys.stdin)['attributed_by_session'].get('\$n',0)>0 else 1)\" || exit 1; done" || blind_ok=0
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
read -r hname hsid < <(sessions | head -1)
htok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$DUMP/$hsid.env"); hsess=$(sed -n 's/^CORAL_SESSION_NAME=//p' "$DUMP/$hsid.env")
# The agent's own board state (written when it joined) names its board and role.
hstate="$DATA/board_state_$hsess.json"
wait_until 30 "the agent's board state ($hsess)" test -s "$hstate" || true
hsub=$(python3 -c "import sys,json;d=json.load(open(sys.argv[1]));print(d.get('subscriber_id') or d.get('job_title',''))" "$hstate" 2>/dev/null || true)
[[ -n "$hsub" ]] || log "hook check: no subscriber in $hstate"
before=$(rjson 'd["attributed_by_tool"].get("coral-hook-message-check",0)')
hook_out=$(echo '{}' | env -u CORAL_URL CORAL_SESSION_TOKEN="$htok" CORAL_SESSION_NAME="$hsess" CORAL_SUBSCRIBER_ID="$hsub" CORAL_DATA_DIR="$DATA" CORAL_URL="$BASE" "$BIN/coral-hook-message-check" 2>&1 || true)
after=$(rjson 'd["attributed_by_tool"].get("coral-hook-message-check",0)')
[[ $after -gt $before ]] || log "hook check: session=$hsess sub=$hsub output=$hook_out"
[[ $after -gt $before ]] && pass "coral-hook-message-check sends the token from its environment and is attributed" || fail "hook not attributed ($before -> $after)"

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
wait_until 30 "all 36 tokens to be reported by their agents" union36 || true
ELAPSED=$((SECONDS - STARTED))
total=$(grep -c . "$ALL_TOKENS" || true)
[[ $total -ge 36 ]] && pass "restarts minted new tokens: $total unique across 3 rounds of 12, no collision" || fail "only $total unique tokens after 2 restart rounds (want 36)"
(( ELAPSED <= BUDGET_S )) && pass "12 agents + 24 restarts in ${ELAPSED}s (budget ${BUDGET_S}s)" || fail "too slow: ${ELAPSED}s > ${BUDGET_S}s"
bad_before=$(rjson 'd["callers"].get("agent_bad_token",0)')
curl -s -m 10 -o /dev/null -H "X-Coral-Session-Token: $OLD" -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x"
bad_after=$(rjson 'd["callers"].get("agent_bad_token",0)')
[[ $bad_after -eq $((bad_before + 1)) ]] && pass "a restarted agent's old token no longer matches" || fail "old token: bad_token $bad_before -> $bad_after"

# ── Wake: sleep then wake mints a new token for the same session ──────
read -r wname wsid < <(sessions | head -1)
wfile="$DUMP/$wsid.env"; before_tok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$wfile" 2>/dev/null || true)
api POST "/api/sessions/live/$wsid/sleep" -d '{}' >/dev/null; sleep 2
api POST "/api/sessions/live/$wsid/wake" -d '{}' >/dev/null
wait_until 60 "the woken agent's new token" bash -c "[[ -f '$wfile' && \$(sed -n 's/^CORAL_SESSION_TOKEN=//p' '$wfile') != '$before_tok' ]]" && pass "wake minted a new token for $wname" || true
collect

# ── Resume: a new token, the old one stops matching ───────────────────
read -r rname rsid < <(sessions | tail -1)
rfile="$DUMP/$rsid.env"; rold=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$rfile" 2>/dev/null || true)
api POST "/api/sessions/live/$rname/resume" -d "{\"session_id\":\"$rsid\",\"current_session_id\":\"$rsid\",\"agent_type\":\"claude\"}" >/dev/null
if wait_until 60 "the resumed agent's new token" bash -c "[[ -f '$rfile' && \$(sed -n 's/^CORAL_SESSION_TOKEN=//p' '$rfile') != '$rold' ]]"; then
    b0=$(rjson 'd["callers"].get("agent_bad_token",0)')
    curl -s -m 10 -o /dev/null -H "X-Coral-Session-Token: $rold" -A "coral-board/test" "$BASE/api/board/id-team-1/messages/check?subscriber_id=x"
    b1=$(rjson 'd["callers"].get("agent_bad_token",0)')
    [[ $b1 -eq $((b0 + 1)) ]] && pass "resume minted a new token and the old one no longer matches" || fail "resume: old token still matched ($b0 -> $b1)"
fi
collect

# ── Leaks: the token is nowhere it shouldn't be ───────────────────────
leaks=0
pslist=$(ps -axo command)
boards=$(for t in 1 2 3; do api GET "/api/board/id-team-$t/messages/all"; done)
globalenv=$(for s in "$DATA"/tmux.sock "$DATA"/*.sock; do [[ -S "$s" ]] && tmux -S "$s" show-environment -g 2>/dev/null; done || true)
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
[[ $leaks -eq 0 ]] && pass "no token in argv, the server log, the data folder (terminal logs, databases), the board or tmux's global env ($(grep -c . "$ALL_TOKENS") tokens checked)"

# ── Impersonation is detected (phase 1: counted and logged, not refused) ──
read -r iname isid < <(sessions | head -1)
itok=$(sed -n 's/^CORAL_SESSION_TOKEN=//p' "$DUMP/$isid.env")
iboard=$(api GET /api/sessions/live | python3 -c "import sys,json;d=json.load(sys.stdin);d=d.get('sessions',d) if isinstance(d,dict) else d;print([s['board_project'] for s in d if s['session_id']=='$isid'][0])")
other=$([[ "$iboard" == id-team-1 ]] && echo id-team-2 || echo id-team-1)
m0=$(rjson 'json.dumps(d["mismatches"])')
post_as() { curl -s -m 10 -o /dev/null -w "%{http_code}" -X POST "$BASE/api/board/$1/messages" -H "Content-Type: application/json" -H "X-Coral-Session-Token: $itok" -A "coral-board/test" -d "{\"subscriber_id\":\"$2\",\"content\":\"impersonation probe\"}"; }
c1=$(post_as "$iboard" "Somebody Else"); c2=$(post_as "$iboard" "Operator")
self=$(api GET /api/sessions/live | python3 -c "import sys,json;d=json.load(sys.stdin);d=d.get('sessions',d) if isinstance(d,dict) else d;print([s.get('display_name') or s['name'] for s in d if s['session_id']=='$isid'][0])")
c3=$(post_as "$other" "$self")
mm=$(rjson 'd["mismatches"]')
python3 - "$m0" "$(rjson 'json.dumps(d["mismatches"])')" <<'PY' && pass "impersonation, Operator and another board each detected once (requests not refused: $c1 $c2 $c3)" || fail "mismatch counts: before $m0 after $mm"
import sys, json
a, b = json.loads(sys.argv[1]), json.loads(sys.argv[2])
d = {k: b.get(k, 0) - a.get(k, 0) for k in ("impersonation", "operator", "cross_board")}
sys.exit(0 if d == {"impersonation": 1, "operator": 1, "cross_board": 1} else 1)
PY
grep -q "\[identity\] mismatch" "$TMP/server.log" && pass "the server log has the mismatch lines" || fail "no mismatch line in the server log"
log "What strict mode would refuse, by caller, tool and route (#3418; this script's own curl calls are local_other curl):"
report | python3 -c '
import sys, json
for k, v in sorted(json.load(sys.stdin).get("would_break_by_route", {}).items(), key=lambda kv: -kv[1]):
    print("[identity]   %5d  %s" % (v, k))'
log "Done: $PASS passed, $FAIL failed"
[[ $FAIL -eq 0 ]]
