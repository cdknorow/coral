#!/usr/bin/env bash
# run_all.sh — #3713/#3740 dashboard latency-budget guard. Opt-in, epoch-only
# (needs the seeded dataset + a CPU hog; it is NOT part of the normal test suite).
# Runs entirely on epoch. Builds the server + seeder, seeds an isolated data dir,
# starts the server, measures the hot endpoints idle and under a CPU hog, checks
# the under-hog p95 against the budgets below, then tears everything down. Kills
# ONLY the PIDs it started. Exit code is non-zero if any p95 budget is breached.
#
# Usage: run_all.sh   (see tests/latency/README.md)
# Env overrides: GO, REPO, WORK, PORT, N, NSESS, NBIG, BIGMB, FILESN, HOG
set -u

# Linux (epoch) only, and opt-in (Security on #3740): this seeds a large dataset, starts real tmux
# sessions and runs a CPU hog, so it must never run on the operator's Mac or in the normal suite.
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "[run_all] the latency guard runs on Linux (epoch) only; got $(uname -s). Refusing." >&2; exit 2
fi
if [[ "${QA_LATENCY_RUN:-}" != "1" ]]; then
  echo "[run_all] opt-in: set QA_LATENCY_RUN=1 to run the latency-budget guard (epoch only)." >&2; exit 2
fi

GO="${GO:-$HOME/go-install/go/bin/go}"
REPO="${REPO:-$HOME/coral}"
CORALGO="$REPO/coral-go"
WORK="${WORK:-$HOME/qa3713-run}"
QA="${QA:-/tmp/qa3713}"           # binaries + data dir (short path for tmux socket)
DATADIR="$QA/data"
PROJECTS="$QA/claude-projects"
PORT="${PORT:-8477}"
N="${N:-100}"
NSESS="${NSESS:-24}"
NBIG="${NBIG:-4}"
BIGMB="${BIGMB:-80}"
FILESN="${FILESN:-5000}"
# The CPU hog is scaled to the box (Security on #3740): 2x the cores (the before/after runs used
# 72 on epoch's 36), so the stress ratio stays the same on any host.
HOG="${HOG:-$(( $(nproc) * 2 ))}"
BASE="http://127.0.0.1:$PORT"
SOCKET="$DATADIR/tmux.sock"

HERE="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$WORK" "$QA"
SERVER_LOG="$WORK/server.log"
HOGPIDS="$WORK/hog.pids"
TARGETS="$WORK/targets.json"
SRVPID=""

log(){ echo "[run_all] $*"; }

cleanup() {
  log "cleanup..."
  if [[ -n "${SRVPID}" ]] && kill -0 "$SRVPID" 2>/dev/null; then kill "$SRVPID" 2>/dev/null; sleep 2; kill -9 "$SRVPID" 2>/dev/null; fi
  if [[ -f "$HOGPIDS" ]]; then while read -r p; do [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null; done < "$HOGPIDS"; fi
  [[ -S "$SOCKET" ]] && "$TMUX_BIN" -S "$SOCKET" kill-server 2>/dev/null
  # Leave ~coral as found: the seeder is copied into the module only for the build (Security on #3740),
  # so drop it here too in case the script died between the copy and its own rm.
  rm -rf "$CORALGO/cmd/qa3713seed" 2>/dev/null
}
trap cleanup EXIT

TMUX_BIN="$(command -v tmux)"
log "go=$("$GO" version) repo HEAD=$(git -C "$REPO" rev-parse HEAD) cores=$(nproc) tmux=$TMUX_BIN"

# ---- build ----
log "building coral + seeder (tags: dev fts5)"
( cd "$CORALGO" && "$GO" build -tags "dev fts5" -o "$QA/coral" ./cmd/coral ) || exit 1
# seeder lives in the module as cmd/qa3713seed (copied in by the caller/this script)
rm -rf "$CORALGO/cmd/qa3713seed"
cp -r "$HERE/seed" "$CORALGO/cmd/qa3713seed"
( cd "$CORALGO" && "$GO" build -tags "dev fts5" -o "$QA/seed" ./cmd/qa3713seed ) || { rm -rf "$CORALGO/cmd/qa3713seed"; exit 1; }
rm -rf "$CORALGO/cmd/qa3713seed"   # leave ~/coral as found

# ---- seed (creates tmux sessions on SOCKET, DBs, transcripts, files) ----
log "seeding dataset: $NSESS sessions ($NBIG x ${BIGMB}MB transcripts), $FILESN file dirs"
"$QA/seed" -datadir "$DATADIR" -projects "$PROJECTS" -board qa3713 \
  -n "$NSESS" -big "$NBIG" -bigmb "$BIGMB" -filesn "$FILESN" \
  -socket "$SOCKET" -tmux "$TMUX_BIN" -out "$TARGETS" || exit 1
cp "$TARGETS" "$WORK/targets.json"

# ---- start server ----
log "starting server on $BASE (isolated data dir, cloud off)"
env -u CORAL_CLOUD_URL \
  CORAL_DATA_DIR="$DATADIR" \
  CLAUDE_PROJECTS_DIR="$PROJECTS" \
  CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 \
  "$QA/coral" --host 127.0.0.1 --port "$PORT" --cloud-url off --backend tmux \
  > "$SERVER_LOG" 2>&1 &
SRVPID=$!
log "server pid=$SRVPID"

# wait for health
for i in $(seq 1 60); do
  if curl -s -o /dev/null "$BASE/api/health"; then break; fi
  kill -0 "$SRVPID" 2>/dev/null || { log "server died; log:"; tail -40 "$SERVER_LOG"; exit 1; }
  sleep 1
done
curl -s "$BASE/api/health"; echo
log "server up. tmux sessions alive: $("$TMUX_BIN" -S "$SOCKET" list-sessions 2>/dev/null | wc -l)"

# sanity: confirm endpoints answer 2xx from loopback (operator)
BOARD_ID=$(python3 -c "import json;print(json.load(open('$TARGETS'))['board_id'])")
for u in \
  "/api/teams/detail/$BOARD_ID/availability" \
  "/api/board/$BOARD_ID/status" ; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -H "Origin: $BASE" -H "Sec-Fetch-Site: same-origin" "$BASE$u")
  log "sanity $u -> $code"
done

# ---- measure idle ----
log "measuring IDLE (N=$N)"
bash "$HERE/measure.sh" "$BASE" "$TARGETS" idle "$N" "$WORK"

# ---- start hog ----
log "starting CPU hog: $HOG busy-loop procs (nice 0)"
: > "$HOGPIDS"
# Each hog carries its own 900 s timeout (Security on #3740): even if this script is killed before
# the trap runs, no busy-loop outlives the run (the #3712 "8 loops left on epoch 10 min" lesson).
for ((i=0;i<HOG;i++)); do timeout 900 sh -c 'while :; do :; done' & echo $! >> "$HOGPIDS"; done
sleep 3
log "hog pids: $(wc -l < "$HOGPIDS") ; load: $(cat /proc/loadavg)"

# ---- measure under hog ----
log "measuring UNDER HOG (N=$N)"
bash "$HERE/measure.sh" "$BASE" "$TARGETS" hog "$N" "$WORK"

# ---- kill hog ----
log "killing hog"
while read -r p; do [[ -n "$p" ]] && kill -9 "$p" 2>/dev/null; done < "$HOGPIDS"
: > "$HOGPIDS"

# ---- combined table ----
log "RESULTS (ms):"
python3 - "$WORK/idle.csv" "$WORK/hog.csv" <<'PY'
import csv,sys
def load(p):
    d={}
    for r in csv.DictReader(open(p)): d[r['endpoint']]=r
    return d
idle=load(sys.argv[1]); hog=load(sys.argv[2])
hdr=f"{'endpoint':<20}{'idle p50':>9}{'p95':>8}{'p99':>8}{'max':>9}   {'hog p50':>9}{'p95':>8}{'p99':>8}{'max':>9}  {'errs':>5}"
print(hdr); print('-'*len(hdr))
for ep in idle:
    i=idle[ep]; h=hog.get(ep,{})
    print(f"{ep:<20}{i['p50_ms']:>9}{i['p95_ms']:>8}{i['p99_ms']:>8}{i['max_ms']:>9}   "
          f"{h.get('p50_ms','-'):>9}{h.get('p95_ms','-'):>8}{h.get('p99_ms','-'):>8}{h.get('max_ms','-'):>9}  "
          f"{h.get('errors','-'):>5}")
PY

# ---- budget check (p95 under hog) ----
log "checking p95 budgets (hog)"
python3 - "$WORK/hog.csv" <<'PY'
import csv,sys
# p95 budgets in ms, under the CPU hog (the stress case; idle is always lower). Set from the
# #3740 after-run on 296439a2 with headroom. chat is 25 FOR NOW: #3723's redact-before-cut over
# tool fields pushed chat p95 up; #3745 makes trimChatPage cheaper and chat returns to 15 then.
BUD={'team_availability':50,'board_status':50,'sleep_status':10,'chat':25,'poll':30,'files':45}
rows={r['endpoint']:r for r in csv.DictReader(open(sys.argv[1]))}
bad=0
for ep,b in BUD.items():
    r=rows.get(ep)
    if not r:
        print(f"  MISSING {ep}"); bad=1; continue
    p95=float(r['p95_ms']); errs=int(r['errors'])
    ok = p95<=b and errs==0
    print(f"  {'PASS' if ok else 'FAIL'} {ep:<20} p95={p95:>6.1f} <= {b:<4} errors={errs}")
    if not ok: bad=1
sys.exit(1 if bad else 0)
PY
BUDGET_RC=$?
if [[ $BUDGET_RC -ne 0 ]]; then log "BUDGET CHECK FAILED (a p95 is over budget)"; else log "all p95 budgets met"; fi
log "done. artifacts in $WORK (idle.csv, hog.csv, targets.json, server.log)"
exit $BUDGET_RC
