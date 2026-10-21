#!/usr/bin/env bash
# profile_busy.sh — #4028: profile a busy Coral server. Opt-in (PERF_RUN=1); meant for epoch, or the
# Mac only when nothing else heavy runs. Builds a dev server, starts it isolated (its own data dir, a
# non-default port, --cloud-url off, pprof on loopback, GODEBUG=gctrace=1), launches 20 mock agents in 3
# teams (they post to and read their boards through coral-board), starts a second Coral in hub mode
# that shows the first as a remote server, and three headless Chrome viewers (desktop on an agent's
# terminal, a 390 px phone on a board, and one through the hub). After WARMUP it takes a CPU profile
# and an allocation profile over PROFILE seconds, records the server's CPU time and the GC share over
# the whole run, and tears everything down, killing only the PIDs it started.
#
# Env: PERF_RUN=1 (required), REPO (default: this checkout), GO, CHROME, PORT (8491), HUBPORT (8492),
# PPROF (8493), MINUTES (15), WARMUP (300 s), PROFILE (120 s), AGENTS (20), OUT (results folder),
# MOCK_TRANSCRIPT (1: the mock agents write transcripts; needs a mock-agent with --mock-transcript),
# IDLE (1: the mock agents do nothing after one turn), TEAMS (3).
set -u
set -o pipefail
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
if [[ "${PERF_RUN:-}" != "1" ]]; then echo "[perf] opt-in: set PERF_RUN=1" >&2; exit 2; fi
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="${REPO:-$(cd "$HERE/../.." && pwd)}"
GO="${GO:-go}"
PORT="${PORT:-8491}"; HUBPORT="${HUBPORT:-8492}"; PPROF="${PPROF:-8493}"
# Whether something listens on port $1, and whether process $1 listens on port $2 (#4346: a health
# check on the port passed against another agent's server and the run drove that one).
port_busy() {
  if [[ "$(uname -s)" == Linux ]]; then ss -ltnH "sport = :$1" | grep -q .
  else netstat -an -p tcp | grep -q "\.$1 .*LISTEN"; fi
}
pid_listens() {
  if [[ "$(uname -s)" == Linux ]]; then ss -ltnpH "sport = :$2" | grep -q "pid=$1,"
  else lsof -nP -a -p "$1" -iTCP:"$2" -sTCP:LISTEN >/dev/null 2>&1; fi
}
for p in "$PORT" "$HUBPORT" "$PPROF"; do
  if [[ "$p" == 8420 ]]; then echo "[perf] refusing port 8420 (the production server)" >&2; exit 2; fi
  if port_busy "$p"; then echo "[perf] port $p is in use" >&2; exit 2; fi
done
MINUTES="${MINUTES:-15}"; WARMUP="${WARMUP:-300}"; PROFILE="${PROFILE:-120}"; AGENTS="${AGENTS:-20}"
BASE_TMP="$([ -d "/sd2/$USER" ] && echo "/sd2/$USER/lead-tmp" || echo "${TMPDIR:-/tmp}")"
mkdir -p "$BASE_TMP" || exit 1
WORK="$(mktemp -d "$BASE_TMP/perf4028.XXXXXX")" || exit 1
[[ -n "$WORK" && -d "$WORK" && "$WORK" != / ]] || { echo "[perf] no work folder" >&2; exit 1; }
OUT="${OUT:-$WORK/results}"
mkdir -p "$OUT" "$WORK/bin" "$WORK/d1" "$WORK/d2" "$WORK/home"
log(){ echo "[perf $(date +%H:%M:%S)] $*"; }

# #4354: an isolated HOME + a stub claude/codex/gemini first on PATH so a mock run can never reach
# the real CLI or write to the real ~/.claude; coral_test_sandbox_check asserts it before exit.
source "$HERE/../lib/mock_sandbox.sh"
coral_test_sandbox

PIDS=()
cleanup() {
  log "cleanup"
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null; done
  sleep 2
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill -9 "$p" 2>/dev/null; done
  for d in "$WORK/d1" "$WORK/d2"; do
    [[ -S "$d/tmux.sock" ]] && tmux -S "$d/tmux.sock" kill-server 2>/dev/null
  done
  # Only inside this run's own mktemp folder: never with an empty or root WORK.
  if [[ "${KEEP_TEST_DATA:-0}" != 1 && -n "$WORK" && -d "$WORK" && "$WORK" != / ]]; then
    # The panes' shells may still be writing their history as tmux stops them: try again once.
    rm -rf -- "$WORK/d1" "$WORK/d2" "$WORK/home" "$WORK/repo" "$WORK/chrome" "$WORK/bin" 2>/dev/null ||
      { sleep 2; rm -rf -- "$WORK/d1" "$WORK/d2" "$WORK/home" "$WORK/repo" "$WORK/chrome" "$WORK/bin"; }
    [[ "$OUT" == "$WORK"/* ]] || rmdir -- "$WORK" 2>/dev/null
  fi
  coral_test_sandbox_cleanup   # #4354: isolated HOME + stub-CLI dir
  log "results in $OUT"
}
trap cleanup EXIT

log "building in $REPO"
( cd "$REPO/coral-go" && "$GO" build -tags "dev fts5" -o "$WORK/bin/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent \
    ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync ./cmd/mock-agent ) || exit 1

git -C "$REPO" rev-parse HEAD > "$OUT/commit.txt"
mkdir -p "$WORK/repo" && git -C "$WORK/repo" init -q -b main && echo hi > "$WORK/repo/README.md" \
  && git -C "$WORK/repo" add . && git -C "$WORK/repo" -c user.name=t -c user.email=t@t commit -qm init

PW="perf-$RANDOM-$RANDOM"
start_server() { # $1 data dir, $2 port, $3 log, rest: extra flags
  local d="$1" port="$2" logf="$3"; shift 3
  printf '%s\n' "$PW" > "$d/pw" && chmod 600 "$d/pw"
  env -u TMUX -u CORAL_CLOUD_URL -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CORAL_SESSION_TOKEN \
    -u CLAUDE_PROJECTS_DIR HOME="$CORAL_TEST_HOME" USERPROFILE="$CORAL_TEST_HOME" PATH="$CORAL_TEST_STUB_BIN:$PATH" CORAL_DATA_DIR="$d" CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 \
    CORAL_INITIAL_PASSWORD_FILE="$d/pw" "$@" \
    "$WORK/bin/coral" --home "$d" --no-browser --host 127.0.0.1 --port "$port" --cloud-url off >"$logf" 2>&1 &
  local pid=$!
  PIDS+=($pid)
  for _ in $(seq 60); do
    kill -0 "$pid" 2>/dev/null || { log "server on $port exited"; tail -20 "$logf"; exit 1; }
    pid_listens "$pid" "$port" && curl -s -m 2 "http://127.0.0.1:$port/api/health" >/dev/null && return 0
    sleep 0.5
  done
  log "server on $port did not start"; tail -20 "$logf"; exit 1
}

log "starting the busy server on $PORT (pprof on $PPROF)"
# MOCK_TRANSCRIPT=1: the mock agents write Claude Code transcripts, as real agents do after their first turn.
MOCK_ENV=(); [[ "${MOCK_TRANSCRIPT:-0}" == 1 ]] && MOCK_ENV=(MOCK_AGENT_TRANSCRIPT=1)
# IDLE=1: the mock agents finish one turn and then do nothing (no posts, reads or output): an idle server.
[[ "${IDLE:-0}" == 1 ]] && MOCK_ENV+=(MOCK_AGENT_IDLE=1)
start_server "$WORK/d1" "$PORT" "$OUT/server.log" CORAL_PPROF_ADDR="127.0.0.1:$PPROF" GODEBUG=gctrace=1 ${MOCK_ENV[@]+"${MOCK_ENV[@]}"} # bash 3.2: an empty array is "unbound" under set -u
SRV=${PIDS[$((${#PIDS[@]} - 1))]}
KEY="$(cat "$WORK/d1/api_key")"
[[ -n "$KEY" ]] || { log "no API key"; exit 1; }
# -m 180: a team launch spaces its agents' starts; a client that gives up first leaves the rest to
# launch after it hung up (#4346: under load they ran without their settings).
api(){ local m="$1" p="$2"; shift 2; curl -s -m 180 -X "$m" "http://127.0.0.1:$PORT$p" -H "X-API-Key: $KEY" -H "Content-Type: application/json" "$@"; }
api PUT /api/settings -d "{\"cli_path_claude\": \"$WORK/bin/mock-agent\"}" >/dev/null

log "launching $AGENTS mock agents in 3 teams"
# TEAMS (default 3): how many teams the agents are split into; the first is "alpha".
TEAMS="${TEAMS:-3}"; per=$(( (AGENTS + TEAMS - 1) / TEAMS )); n=0
team_names=(alpha); for i in $(seq 2 "$TEAMS"); do team_names+=("team$i"); done
for team in "${team_names[@]}"; do
  agents=""
  for i in $(seq "$per"); do
    [[ $n -ge $AGENTS ]] && break
    n=$((n + 1))
    agents="$agents${agents:+,}{\"agent_type\":\"claude\",\"name\":\"$team-$i\",\"prompt\":\"You are agent $i of $team.\"}"
  done
  api POST /api/sessions/launch-team -d "{\"board_name\":\"$team\",\"working_dir\":\"$WORK/repo\",\"agents\":[$agents]}" > "$OUT/launch-$team.json"
done
sleep 15
api GET /api/sessions/live > "$OUT/live.json"
live=$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); s=d if isinstance(d,list) else d.get("sessions",[]); print(len(s))' "$OUT/live.json")
log "$live live sessions"

log "starting the hub-mode Coral on $HUBPORT"
start_server "$WORK/d2" "$HUBPORT" "$OUT/hub.log" CORAL_HUB=1
HKEY="$(cat "$WORK/d2/api_key")"
[[ -n "$HKEY" ]] || { log "no hub API key"; exit 1; }
curl -s -m 20 -X POST "http://127.0.0.1:$HUBPORT/api/servers" -H "X-API-Key: $HKEY" -H "Content-Type: application/json" \
  -d "{\"id\":\"busy\",\"label\":\"busy\",\"url\":\"http://127.0.0.1:$PORT\",\"api_key\":\"$KEY\",\"allow_private\":true}" > "$OUT/hub-add.json"

AGENT=$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); s=d if isinstance(d,list) else d.get("sessions",[]); print(s[0].get("session_id") or "")' "$OUT/live.json")
BOARD=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("board_id",""))' "$OUT/launch-alpha.json")
log "viewers: desktop on /agents/$AGENT (a session id), phone on /board/$BOARD, hub on /agents"
node "$HERE/viewers.mjs" --modules "${MODULES:-$REPO/tests/frontend}" --chrome "${CHROME:-}" --profile-dir "$WORK/chrome" --password "$PW" \
  --tab "desktop,1440,900,http://127.0.0.1:$PORT,/agents/$AGENT" \
  --tab "phone,390,844,http://127.0.0.1:$PORT,/board/$BOARD" \
  --tab "hub,1280,800,http://127.0.0.1:$HUBPORT,/agents" > "$OUT/viewers.log" 2>&1 &
PIDS+=($!)

# The server's CPU seconds so far: Linux (procps) prints them as "times"; macOS only as
# [[dd-]hh:]mm:ss.cc in "time".
cpu_seconds(){
  if [[ "$(uname -s)" == Linux ]]; then ps -o times= -p "$SRV" | tr -d ' '
  else ps -o time= -p "$SRV" | awk -F: '{ s = 0; for (i = 1; i <= NF; i++) s = s * 60 + $i; printf "%d\n", s }'; fi
}
T0=$(date +%s); C0=$(cpu_seconds)
log "warming up ${WARMUP}s"
sleep "$WARMUP"
curl -s -m 10 "http://127.0.0.1:$PPROF/debug/coral/tmux-execs" -o "$OUT/tmux-execs-start.json"
log "profiling ${PROFILE}s"
curl -s -m $((PROFILE + 30)) "http://127.0.0.1:$PPROF/debug/pprof/profile?seconds=$PROFILE" -o "$OUT/cpu.pprof" &
P1=$!
curl -s -m $((PROFILE + 30)) "http://127.0.0.1:$PPROF/debug/pprof/allocs?seconds=$PROFILE" -o "$OUT/allocs.pprof" &
P2=$!
wait "$P1" "$P2" # by pid: %1 and %2 would be the first jobs started, the servers
curl -s -m 10 "http://127.0.0.1:$PPROF/debug/coral/tmux-execs" -o "$OUT/tmux-execs-end.json"
REST=$(( MINUTES * 60 - WARMUP - PROFILE ))
[[ $REST -gt 0 ]] && { log "running ${REST}s more"; sleep "$REST"; }
T1=$(date +%s); C1=$(cpu_seconds)
curl -s -m 30 "http://127.0.0.1:$PPROF/debug/pprof/heap" -o "$OUT/heap.pprof"
curl -s -m 30 "http://127.0.0.1:$PPROF/debug/pprof/goroutine?debug=1" -o "$OUT/goroutines.txt"
log "agent transcripts written: $(find "$CORAL_TEST_HOME/.claude/projects" -name '*.jsonl' 2>/dev/null | wc -l | tr -d ' ')"

PROFILE="$PROFILE" python3 -I - "$OUT" "$T0" "$T1" "$C0" "$C1" <<'PY' | tee "$OUT/summary.txt"
import re, sys
out, t0, t1, c0, c1 = sys.argv[1], *map(float, sys.argv[2:])
wall = t1 - t0
print(f"wall {wall:.0f} s; server CPU {c1 - c0:.0f} s = {100 * (c1 - c0) / wall:.1f}% of one core")
import json, os
try:
    a = json.load(open(f"{out}/tmux-execs-start.json")); b = json.load(open(f"{out}/tmux-execs-end.json"))
    win = float(os.environ.get("PROFILE", "120")) / 60
    per = {k: (b.get(k, 0) - a.get(k, 0)) / win for k in b}
    # "capture-pane<caller>" splits capture-pane by the code that asked (#4077): not in the total.
    print("tmux execs per minute (profile window): total %.0f; %s" % (sum(v for k, v in per.items() if "<" not in k),
          ", ".join(f"{k} {v:.0f}" for k, v in sorted(per.items(), key=lambda kv: -kv[1]) if v)))
except Exception as e:
    print(f"tmux execs: not available ({e})")
gc = [l for l in open(f"{out}/server.log", errors="replace") if l.startswith("gc ")]
if gc:
    m = re.search(r"@[\d.]+s (\d+)%", gc[-1])
    print(f"GC: {len(gc)} cycles; {m.group(1) if m else '?'}% of CPU time since start (gctrace)")
PY
GOBIN_TOOL="$GO"
for kind in cpu allocs; do
  "$GOBIN_TOOL" tool pprof -top -nodecount=25 "$WORK/bin/coral" "$OUT/$kind.pprof" > "$OUT/$kind-top.txt" 2>&1
done
"$GOBIN_TOOL" tool pprof -top -sample_index=alloc_space -nodecount=25 "$WORK/bin/coral" "$OUT/allocs.pprof" > "$OUT/allocs-space-top.txt" 2>&1
"$GOBIN_TOOL" tool pprof -top -cum -nodecount=40 "$WORK/bin/coral" "$OUT/cpu.pprof" > "$OUT/cpu-cum.txt" 2>&1
cp "$WORK/bin/coral" "$OUT/coral.bin"
# #4354: no real CLI was reached and the real ~/.claude did not grow; fail the run loudly otherwise.
coral_test_sandbox_check || exit 1
log "done: $OUT (summary.txt, cpu-top.txt, cpu-cum.txt, allocs-top.txt, allocs-space-top.txt)"
