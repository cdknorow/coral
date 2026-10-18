#!/usr/bin/env bash
# scale.sh — #4598: how far one Coral scales with many agents. Opt-in (PERF_RUN=1); meant for epoch.
# Builds a dev server, starts it isolated (own data dir, non-default port, --cloud-url off, pprof on
# loopback with block and mutex profiles), launches AGENTS mock agents in teams of 25 (they post to and
# read their boards through coral-board: a busy board), then drives it with TABS simulated dashboard tabs
# and a board poster (scale_load.py) while it samples the server process and takes a CPU profile, then
# a heap, allocs, mutex, block and goroutine profile. Results land in OUT: load.json (p50/p95/p99 per
# poll endpoint, WebSocket frames and bytes per tab, CPU, RSS, threads, goroutines), cpu-top.txt,
# mutex-top.txt, block-top.txt, allocs-top.txt, goroutines.txt, lock-errors.txt, commit.txt. Kills only the
# PIDs it started; removes its own work folder on exit. scale_table.py turns several OUT folders into one table.
#
# Env: PERF_RUN=1 (required), AGENTS (25), TABS (3), LOAD (120 s), WARMUP (60 s), CPU (30 s),
# POST_RATE (board posts per second, 2), GO, REPO, PORT (8497), PPROF (8498), OUT, KEEP_TEST_DATA.
set -u
set -o pipefail
if [[ "${PERF_RUN:-}" != "1" ]]; then echo "[scale] opt-in: set PERF_RUN=1" >&2; exit 2; fi
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="${REPO:-$(cd "$HERE/../.." && pwd)}"
GO="${GO:-go}"
PORT="${PORT:-8497}"; PPROF="${PPROF:-8498}"
AGENTS="${AGENTS:-25}"; TABS="${TABS:-3}"; LOAD="${LOAD:-120}"; WARMUP="${WARMUP:-60}"; CPU="${CPU:-30}"; POST_RATE="${POST_RATE:-2}"
port_busy() {
  if [[ "$(uname -s)" == Linux ]]; then ss -ltnH "sport = :$1" | grep -q .
  else netstat -an -p tcp | grep -q "\.$1 .*LISTEN"; fi
}
pid_listens() {
  if [[ "$(uname -s)" == Linux ]]; then ss -ltnpH "sport = :$2" | grep -q "pid=$1,"
  else lsof -nP -a -p "$1" -iTCP:"$2" -sTCP:LISTEN >/dev/null 2>&1; fi
}
for p in "$PORT" "$PPROF"; do
  if [[ "$p" == 8420 ]]; then echo "[scale] refusing port 8420 (the production server)" >&2; exit 2; fi
  if port_busy "$p"; then echo "[scale] port $p is in use" >&2; exit 2; fi
done
BASE_TMP="$([ -d "/sd2/$USER" ] && echo "/sd2/$USER/be-tmp" || echo "${TMPDIR:-/tmp}")"
mkdir -p "$BASE_TMP" || exit 1
WORK="$(mktemp -d "$BASE_TMP/scale4598.XXXXXX")" || exit 1
[[ -n "$WORK" && -d "$WORK" && "$WORK" != / ]] || { echo "[scale] no work folder" >&2; exit 1; }
OUT="${OUT:-$WORK/results}"
mkdir -p "$OUT" "$WORK/bin" "$WORK/d1" "$WORK/home"
log(){ echo "[scale $(date +%H:%M:%S)] $*"; }
source "$HERE/../lib/mock_sandbox.sh"   # #4354: isolated HOME + stub CLIs; checked before exit
coral_test_sandbox

PIDS=()
cleanup() {
  log "cleanup"
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null; done
  sleep 2
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill -9 "$p" 2>/dev/null; done
  [[ -S "$WORK/d1/tmux.sock" ]] && tmux -S "$WORK/d1/tmux.sock" kill-server 2>/dev/null
  if [[ "${KEEP_TEST_DATA:-0}" != 1 && -n "$WORK" && -d "$WORK" && "$WORK" != / ]]; then
    rm -rf -- "$WORK/d1" "$WORK/home" "$WORK/repo" "$WORK/bin" 2>/dev/null || { sleep 2; rm -rf -- "$WORK/d1" "$WORK/home" "$WORK/repo" "$WORK/bin"; }
    [[ "$OUT" == "$WORK"/* ]] || rmdir -- "$WORK" 2>/dev/null
  fi
  coral_test_sandbox_cleanup
  log "results in $OUT"
}
trap cleanup EXIT

log "building in $REPO"
( cd "$REPO/coral-go" && "$GO" build -tags "dev fts5" -o "$WORK/bin/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent \
    ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync ./cmd/mock-agent ) || exit 1
{ git -C "$REPO" rev-parse HEAD 2>/dev/null || cat "$REPO/COMMIT" 2>/dev/null || echo unknown; } > "$OUT/commit.txt"
mkdir -p "$WORK/repo" && git -C "$WORK/repo" init -q -b main && echo hi > "$WORK/repo/README.md" \
  && git -C "$WORK/repo" add . && git -C "$WORK/repo" -c user.name=t -c user.email=t@t commit -qm init

log "starting the server on $PORT (pprof on $PPROF)"
env -u TMUX -u CORAL_CLOUD_URL -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CORAL_SESSION_TOKEN -u CLAUDE_PROJECTS_DIR \
  HOME="$CORAL_TEST_HOME" PATH="$CORAL_TEST_STUB_BIN:$PATH" CORAL_DATA_DIR="$WORK/d1" CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 \
  CORAL_PPROF_ADDR="127.0.0.1:$PPROF" \
  "$WORK/bin/coral" --home "$WORK/d1" --no-browser --host 127.0.0.1 --port "$PORT" --cloud-url off >"$OUT/server.log" 2>&1 &
SRV=$!
PIDS+=($SRV)
for _ in $(seq 120); do
  kill -0 "$SRV" 2>/dev/null || { log "server exited"; tail -20 "$OUT/server.log"; exit 1; }
  pid_listens "$SRV" "$PORT" && curl -s -m 2 "http://127.0.0.1:$PORT/api/health" >/dev/null && break
  sleep 0.5
done
pid_listens "$SRV" "$PORT" || { log "server did not start"; exit 1; }
KEY="$(cat "$WORK/d1/api_key" 2>/dev/null)"
[[ -n "$KEY" ]] || { log "no API key"; exit 1; }
api(){ local m="$1" p="$2"; shift 2; curl -s -m 600 -X "$m" "http://127.0.0.1:$PORT$p" -H "X-API-Key: $KEY" -H "Content-Type: application/json" "$@"; }
api PUT /api/settings -d "{\"cli_path_claude\": \"$WORK/bin/mock-agent\"}" >/dev/null

TEAMS=$(( (AGENTS + 24) / 25 )); per=$(( (AGENTS + TEAMS - 1) / TEAMS )); n=0
log "launching $AGENTS mock agents in $TEAMS team(s)"
T_LAUNCH=$(date +%s)
for t in $(seq "$TEAMS"); do
  agents=""
  for i in $(seq "$per"); do
    [[ $n -ge $AGENTS ]] && break
    n=$((n + 1))
    agents="$agents${agents:+,}{\"agent_type\":\"claude\",\"name\":\"t$t-a$i\",\"prompt\":\"You are agent $i of team $t.\"}"
  done
  api POST /api/sessions/launch-team -d "{\"board_name\":\"scale$t\",\"working_dir\":\"$WORK/repo\",\"agents\":[$agents]}" > "$OUT/launch-$t.json"
done
echo "$(( $(date +%s) - T_LAUNCH ))" > "$OUT/launch-seconds.txt"
sleep "$WARMUP"
api GET /api/sessions/live > "$OUT/live.json"
live=$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); s=d if isinstance(d,list) else d.get("sessions",[]); print(len(s))' "$OUT/live.json")
log "$live live sessions (launch took $(cat "$OUT/launch-seconds.txt") s)"
echo "$live" > "$OUT/live-count.txt"
echo "load average at the start: $(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || uptime)" > "$OUT/machine.txt"

log "load: $TABS tabs, posts $POST_RATE/s, ${LOAD}s; CPU profile ${CPU}s from the middle"
( sleep $(( LOAD / 2 - CPU / 2 )); curl -s -m $((CPU + 30)) "http://127.0.0.1:$PPROF/debug/pprof/profile?seconds=$CPU" -o "$OUT/cpu.pprof" ) &
CPUJOB=$!
printf '%s\n' "$KEY" > "$WORK/key.txt"; chmod 600 "$WORK/key.txt"
python3 -I "$HERE/scale_load.py" "http://127.0.0.1:$PORT" "$WORK/key.txt" "$SRV" "$PPROF" "$TABS" "$LOAD" "$OUT/load.json" "$POST_RATE" | tee "$OUT/load-summary.txt"
wait "$CPUJOB"
for k in heap allocs mutex block; do curl -s -m 60 "http://127.0.0.1:$PPROF/debug/pprof/$k" -o "$OUT/$k.pprof"; done
curl -s -m 30 "http://127.0.0.1:$PPROF/debug/pprof/goroutine?debug=1" -o "$OUT/goroutines.txt"
grep -ciE "database is locked|SQLITE_BUSY|busy" "$OUT/server.log" > "$OUT/lock-errors-count.txt"; grep -iE "database is locked|SQLITE_BUSY" "$OUT/server.log" | head -20 > "$OUT/lock-errors.txt"
for kind in cpu mutex block; do
  "$GO" tool pprof -top -cum -nodecount=30 "$WORK/bin/coral" "$OUT/$kind.pprof" > "$OUT/$kind-top.txt" 2>&1
done
"$GO" tool pprof -top -nodecount=30 "$WORK/bin/coral" "$OUT/cpu.pprof" > "$OUT/cpu-flat.txt" 2>&1
"$GO" tool pprof -top -sample_index=alloc_space -nodecount=30 "$WORK/bin/coral" "$OUT/allocs.pprof" > "$OUT/allocs-top.txt" 2>&1
"$GO" tool pprof -top -nodecount=20 "$WORK/bin/coral" "$OUT/heap.pprof" > "$OUT/heap-top.txt" 2>&1
cp "$WORK/bin/coral" "$OUT/coral.bin"
coral_test_sandbox_check || exit 1
log "done: $OUT"
