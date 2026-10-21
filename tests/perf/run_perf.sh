#!/usr/bin/env bash
# run_perf.sh — #3851 reusable performance baseline (supports #3845-#3847).
#
# One command, run on epoch, that reproduces the #3833 measurement each time and prints the
# SAME table: startup-to-serving, CPU per component (pprof top), p50/p95 per hot endpoint
# (idle and under two dashboard tabs), and the fork/exec rate (execve census). It merges
# Security's #3833 load setup (bulk dataset, tab simulator, pprof, strace) with QA's #3713
# latency harness (the seeder and measure.sh under tests/latency/). Measurement only; no
# product change. It never touches ~/.coral or :8420; the server runs with an isolated
# CORAL_DATA_DIR, CORAL_CLOUD_URL unset and --cloud-url off. It kills ONLY the PIDs it
# started (no pattern-kills) and removes its temp dirs.
#
# Usage (epoch):
#   QA_PERF_RUN=1 bash tests/perf/run_perf.sh                 # against ~/coral
#   QA_PERF_RUN=1 REPO=/path/to/worktree WORK=$HOME/perf-run bash tests/perf/run_perf.sh
# Data lands under /sd2/$USER/qa-tmp on epoch (QA_BASE), removed on exit; KEEP_PERF_DATA=1 keeps it.
# Env overrides: GO, REPO, WORK, QA, QA_BASE, KEEP_PERF_DATA, PORT, PPROF_PORT, N, NSESS, NBIG, BIGMB, SMALLKB,
#   FILESN, NMSG, NEVENT, TABS, WARMUP_S, CPU_S, LOAD_S, STRACE_S, STRACE_SETTLE_S.
# The script re-execs itself under `nice -n 10` (one documented command, nice +10).
set -u
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)

# Linux (epoch) only, opt-in: it seeds a large dataset, starts real tmux sessions, runs pprof
# and strace, so it must never run on the operator's Mac or in the normal suite.
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "[perf] runs on Linux (epoch) only; got $(uname -s). Refusing." >&2; exit 2
fi
if [[ "${QA_PERF_RUN:-}" != "1" ]]; then
  echo "[perf] opt-in: set QA_PERF_RUN=1 to run the perf baseline (epoch only)." >&2; exit 2
fi
# nice +10 for the whole run (re-exec once).
if [[ "${PERF_NICED:-}" != "1" ]]; then
  export PERF_NICED=1
  exec nice -n 10 bash "$0" "$@"
fi

GO="${GO:-$HOME/go-install/go/bin/go}"
REPO="${REPO:-$HOME/coral}"
CORALGO="$REPO/coral-go"
# #4354 mock-agent sandbox: this harness starts a server and sessions, so it runs under an isolated
# HOME with a stub for every agent CLI first on PATH; a CLI reach falls on the stub, never the
# operator's real CLI, and the end check fails the run if the stub was hit.
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/mock_sandbox.sh"
# Epoch rule (see the team's internal notes): keep temp + seeded data under /sd2/$USER/qa-tmp (830 GB free,
# not the ~92%-full /), delete on exit. Fall back to /tmp when /sd2 is absent (a non-epoch box).
QA_BASE="${QA_BASE:-$([ -d "/sd2/$USER" ] && echo "/sd2/$USER/qa-tmp" || echo /tmp)}"
mkdir -p "$QA_BASE" 2>/dev/null || true
WORK="${WORK:-$QA_BASE/qa3851-run}"
QA="${QA:-$QA_BASE/qa3851}"             # binaries + data dir (short path for tmux socket)
DATADIR="$QA/data"
PROJECTS="$QA/claude-projects"
PORT="${PORT:-8487}"
PPROF_PORT="${PPROF_PORT:-6081}"
N="${N:-100}"                            # measure.sh samples per endpoint
NSESS="${NSESS:-40}"
NBIG="${NBIG:-4}"
BIGMB="${BIGMB:-40}"
SMALLKB="${SMALLKB:-256}"
FILESN="${FILESN:-2000}"
NMSG="${NMSG:-300000}"
NEVENT="${NEVENT:-250000}"
TABS="${TABS:-2}"
WARMUP_S="${WARMUP_S:-150}"
CPU_S="${CPU_S:-60}"
LOAD_S="${LOAD_S:-180}"
STRACE_S="${STRACE_S:-120}"
STRACE_SETTLE_S="${STRACE_SETTLE_S:-90}"   # let startup churn (per-agent login/uname probes) finish before the idle execve window
BASE="http://127.0.0.1:$PORT"
PPROF="http://127.0.0.1:$PPROF_PORT"
SOCKET="$DATADIR/tmux.sock"

HERE="$(cd "$(dirname "$0")" && pwd)"
LAT="$HERE/../latency"                   # the seeder and measure.sh live here (on main)
OUT="$WORK/out"
mkdir -p "$WORK" "$QA" "$OUT"
TARGETS="$WORK/targets.json"
SERVER_LOG="$WORK/server.log"
TMUX_BIN="$(command -v tmux)"
SRVPID=""; LOADPID=""; SAMPLEPID=""; STRACEPID=""

log(){ echo "[perf] $*"; }

cleanup() {
  log "cleanup (recorded PIDs only)..."
  for v in LOADPID SAMPLEPID STRACEPID; do
    p="${!v}"
    [[ -n "$p" ]] && kill -0 "$p" 2>/dev/null && kill "$p" 2>/dev/null
  done
  stop_server   # kills the server's whole process group (reaps the coral child under strace too)
  [[ -S "$SOCKET" ]] && "$TMUX_BIN" -S "$SOCKET" kill-server 2>/dev/null
  rm -rf "$CORALGO/cmd/qa3851seed" 2>/dev/null   # leave the repo as found (seeder copy)
  # Delete the run's data on exit (see the team's internal notes); the table is on stdout. Keep with KEEP_PERF_DATA=1.
  if [[ "${KEEP_PERF_DATA:-}" != "1" ]]; then
    rm -rf "$QA" "$WORK" 2>/dev/null
  else
    log "KEEP_PERF_DATA=1: left $QA and $WORK"
  fi
  coral_test_sandbox_cleanup   # #4354: isolated HOME + stub-CLI dir (runs the check too on a crash)
}
trap cleanup EXIT

# start_server LABEL [strace_outfile] -> sets SRVPID; waits for health; records startup-to-serving.
start_server() {
  local label="$1" straceout="${2:-}"
  : > "$SERVER_LOG"
  local env_cmd=(env -u CORAL_CLOUD_URL GODEBUG=gctrace=1
    HOME="$CORAL_TEST_HOME" USERPROFILE="$CORAL_TEST_HOME" PATH="$CORAL_TEST_STUB_BIN:$PATH"
    CORAL_DATA_DIR="$DATADIR" CLAUDE_PROJECTS_DIR="$PROJECTS"
    CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 CORAL_DEBUG=1
    CORAL_PPROF_ADDR="127.0.0.1:$PPROF_PORT")
  local t0 t1
  t0=$(date +%s.%N)
  if [[ -n "$straceout" ]]; then
    setsid "${env_cmd[@]}" strace -f -qq -e trace=execve -o "$straceout" \
      "$QA/coral" --host 127.0.0.1 --port "$PORT" --cloud-url off --backend tmux \
      < /dev/null > "$SERVER_LOG" 2>&1 &
  else
    setsid "${env_cmd[@]}" \
      "$QA/coral" --host 127.0.0.1 --port "$PORT" --cloud-url off --backend tmux \
      < /dev/null > "$SERVER_LOG" 2>&1 &
  fi
  SRVPID=$!
  local up=""
  for i in $(seq 1 180); do
    if curl -s -o /dev/null "$BASE/api/health"; then up=1; t1=$(date +%s.%N); break; fi
    kill -0 "$SRVPID" 2>/dev/null || { log "server ($label) died; log tail:"; tail -30 "$SERVER_LOG"; return 1; }
    sleep 1
  done
  [[ -n "$up" ]] || { log "server ($label) never served health"; return 1; }
  # Strict local auth is the default (#3936): load.py sends this server's own API key.
  export CORAL_API_KEY; CORAL_API_KEY="$(tr -d '[:space:]' < "$DATADIR/api_key")"
  awk "BEGIN{printf \"%.2f\", $t1-$t0}" > "$OUT/startup_${label}.txt"
  log "server ($label) pid=$SRVPID startup-to-serving=$(cat "$OUT/startup_${label}.txt")s"
}

stop_server() {
  # setsid made SRVPID the process-group leader, so kill the whole group, not just SRVPID:
  # under strace, SRVPID is the strace wrapper and the real coral is its traced child in the
  # same group, so killing only SRVPID would orphan coral. The server runs in its own session
  # (setsid), separate from this script's group, so this never signals the harness itself.
  if [[ -n "$SRVPID" ]] && kill -0 "$SRVPID" 2>/dev/null; then
    kill -TERM -- "-$SRVPID" 2>/dev/null; sleep 3; kill -KILL -- "-$SRVPID" 2>/dev/null
  fi
  SRVPID=""
}

snap() { for p in heap allocs goroutine; do curl -s -m 20 -o "$OUT/${p}_$1.pprof" "$PPROF/debug/pprof/$p"; done; }

log "go=$("$GO" version | awk '{print $3}') repo HEAD=$(git -C "$REPO" rev-parse --short HEAD) cores=$(nproc) load=$(cat /proc/loadavg)"

# ---- build coral + seeder (tags: dev fts5) ----
log "building coral + seeder"
( cd "$CORALGO" && "$GO" build -tags "dev fts5" -o "$QA/coral" ./cmd/coral ) || exit 1
rm -rf "$CORALGO/cmd/qa3851seed"; cp -r "$LAT/seed" "$CORALGO/cmd/qa3851seed"
( cd "$CORALGO" && "$GO" build -tags "dev fts5" -o "$QA/seed" ./cmd/qa3851seed ) || { rm -rf "$CORALGO/cmd/qa3851seed"; exit 1; }
rm -rf "$CORALGO/cmd/qa3851seed"

# ---- seed sessions + dataset ----
log "seeding $NSESS sessions ($NBIG x ${BIGMB}MB transcripts), $FILESN file dirs"
"$QA/seed" -datadir "$DATADIR" -projects "$PROJECTS" -board qa3851 \
  -n "$NSESS" -big "$NBIG" -bigmb "$BIGMB" -smallkb "$SMALLKB" -filesn "$FILESN" \
  -socket "$SOCKET" -tmux "$TMUX_BIN" -out "$TARGETS" || exit 1
log "bulk: $NMSG board messages, $NEVENT agent events (server stopped)"
python3 "$HERE/bulk.py" "$DATADIR" "$TARGETS" "$NMSG" "$NEVENT" || exit 1

# ============ Phase A: idle ============
coral_test_sandbox   # #4354: isolated HOME + stub-CLI PATH for every start_server below
log "=== Phase A (idle) ==="
start_server A || exit 1
log "warm-up ${WARMUP_S}s (indexer, pollers)"; sleep "$WARMUP_S"
( for i in $(seq 1 24); do ps -o %cpu=,rss=,nlwp= -p "$SRVPID" | awk -v t="$(date +%T)" '{print t, $1, $2, $3}' >> "$OUT/ps_A.txt"; sleep 5; done ) & SAMPLEPID=$!
curl -s -m $((CPU_S+30)) -o "$OUT/cpu_A.pprof" "$PPROF/debug/pprof/profile?seconds=$CPU_S"
wait "$SAMPLEPID" 2>/dev/null; SAMPLEPID=""
snap A
log "idle load: $(cat /proc/loadavg)"

# ============ Phase B: two dashboard tabs ============
log "=== Phase B ($TABS dashboard tabs, ${LOAD_S}s) ==="
python3 "$HERE/load.py" "$BASE" "$TARGETS" "$TABS" "$LOAD_S" > "$OUT/tabs_B.txt" 2>&1 & LOADPID=$!
( for i in $(seq 1 $((LOAD_S/5)) ); do ps -o %cpu=,rss=,nlwp= -p "$SRVPID" | awk -v t="$(date +%T)" '{print t, $1, $2, $3}' >> "$OUT/ps_B.txt"; sleep 5; done ) & SAMPLEPID=$!
sleep 20
curl -s -m $((CPU_S+30)) -o "$OUT/cpu_B.pprof" "$PPROF/debug/pprof/profile?seconds=$CPU_S"
# measure p50/p95 per endpoint while the tabs are still running
bash "$LAT/measure.sh" "$BASE" "$TARGETS" load "$N" "$OUT"
wait "$LOADPID" 2>/dev/null; LOADPID=""
wait "$SAMPLEPID" 2>/dev/null; SAMPLEPID=""
snap B
log "loaded load: $(cat /proc/loadavg)"
# also measure once idle (no tabs) for the idle column
bash "$LAT/measure.sh" "$BASE" "$TARGETS" idle "$N" "$OUT"
grep -E "^gc [0-9]+ @" "$SERVER_LOG" > "$OUT/gctrace.txt" 2>/dev/null || true
stop_server

# ============ Phase C: fork/exec census (server under strace) ============
log "=== Phase C (execve census under strace, idle ${STRACE_S}s + load ${STRACE_S}s) ==="
start_server C "$OUT/strace_C.txt" || exit 1
# strace -f writes more than execve lines (attach notes, "+++ exited +++", "--- SIGCHLD ---",
# "<... resumed>"), so count only the real execve calls, not every line.
nexecve(){ grep -c 'execve(' "$OUT/strace_C.txt" 2>/dev/null || true; }
# Let per-agent startup churn (login/uname/version probes) settle so the idle window is steady state.
log "settle ${STRACE_SETTLE_S}s before the idle execve window"; sleep "$STRACE_SETTLE_S"
log "idle execve window ${STRACE_S}s"; m1=$(nexecve); sleep "$STRACE_S"; m2=$(nexecve)
echo "idle $((m2-m1))" > "$OUT/execve_counts.txt"
python3 "$HERE/load.py" "$BASE" "$TARGETS" "$TABS" "$STRACE_S" > "$OUT/tabs_C.txt" 2>&1 & LOADPID=$!
m3=$(nexecve); wait "$LOADPID" 2>/dev/null; LOADPID=""; m4=$(nexecve)
echo "load $((m4-m3))" >> "$OUT/execve_counts.txt"
stop_server

# ============ The table ============
log "========================= BASELINE TABLE ========================="
coral_test_sandbox_check || { log "FAIL (#4354): a real agent CLI on PATH was reached during the perf run"; exit 1; }
GO="$GO" python3 "$HERE/table.py" "$OUT" "$QA/coral" "$STRACE_S" "$TABS" "$NSESS" "$NMSG" "$NEVENT" "$(cat /proc/loadavg)"
log "artifacts in $OUT (cpu_*.pprof, *.csv, strace_C.txt, tabs_*.txt, ps_*.txt) — removed on exit unless KEEP_PERF_DATA=1"
