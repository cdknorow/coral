#!/usr/bin/env bash
# update_rehearsal.sh: the update chain run for real on a Linux runner, without a promoted release (#5401).
#
#   tests/release/update_rehearsal.sh [--keep]
#
# Builds release N and release N+1 from this source (the programs of tests/release/commands.txt AND coral-update, which no release package carries yet:
# the layout DevOps's packaging change is to give), installs N into a folder of its own, starts the REAL server of N there (fresh HOME and data folder,
# a random port, CORAL_CLOUD_URL=off), and runs the updater's real code against it through internal/updater/testrig/cmd/rehearsal-update: stage.Check
# (fetch from a fake GitHub on loopback, verify against a fake Sigstore with signed certificate timestamps, log entry and timestamp, stage) and apply.Run
# (verify again, strict unpack, 64-bit ELF checks, the new coral's --version, atomic exchange, start, health, rollback), with the real Linux platform. The
# real coral-update cannot be pointed at a fake world (a test holds that), so the program that is is the rehearsal's own and is never released.
#
#   1 update      the server of N is replaced: N+1 answers /api/health, the old process is gone, the install says N+1, one server runs, the board
#                 message made before is still there and the second start of N+1 left the data readable
#   2 tampered    a bit flipped in the package on the wire: refused at the package stage, nothing staged, the server of N untouched and still the one running
#   3 wrong       a package whose programs say another version than the release: refused after the unpack, the install byte for byte as it was
#   4 unhealthy   N+1 passes every check and does not stay up: rolled back, N is back at the install path and answers /api/health again
#   5 crash       the apply dies right after the swap: the next apply leaves a consistent install (N or N+1, never half) and a server starts from it
#
# Output: one table; exit status 0 only when no item FAILs. Processes are stopped by pid this script recorded or found under its own install folders; the
# work folder is removed by its own path (--keep leaves it). Linux only; needs go, curl, python3, tar. Nothing is sent anywhere.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
keep=0
while [ $# -gt 0 ]; do
  case "$1" in
    --keep) keep=1; shift ;;
    -h|--help) sed -n 2,28p "$0"; exit 0 ;;
    *) echo "update_rehearsal: unknown option $1" >&2; exit 2 ;;
  esac
done
[ "$(uname -s)" = Linux ] || { echo "update_rehearsal: Linux only (the apply uses renameat2 and /proc)" >&2; exit 2; }
for t in go curl python3 tar; do command -v "$t" >/dev/null || { echo "update_rehearsal: needs $t" >&2; exit 2; }; done
root=$(cd "$here/../.." && pwd)
N=1.4.900 N1=1.4.901 NBAD=1.4.902

work=$(mktemp -d /tmp/coral-rhs.XXXXXX) || exit 2
PIDS=()
sweep() { # stop every process whose executable is under one of this run's own folders, and every recorded pid
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  for d in /proc/[0-9]*; do
    exe=$(readlink "$d/exe" 2>/dev/null) || continue
    case "$exe" in "$work"/*) kill "${d#/proc/}" 2>/dev/null || true ;; esac
  done
  sleep 1
  for d in /proc/[0-9]*; do
    exe=$(readlink "$d/exe" 2>/dev/null) || continue
    case "$exe" in "$work"/*) kill -9 "${d#/proc/}" 2>/dev/null || true ;; esac
  done
}
cleanup() {
  sweep
  if [ "$keep" = 1 ]; then echo "update_rehearsal: kept $work"; else case "$work" in /tmp/coral-rhs.??????) rm -rf -- "$work" ;; esac; fi
}
trap cleanup EXIT

NAMES=() STATES=() DETAILS=()
result() { NAMES+=("$1"); STATES+=("$2"); DETAILS+=("$3"); [ "$2" = PASS ] || echo "update_rehearsal: $2 $1: $3" >&2; }
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
jget() { python3 -c 'import sys,json
try:
    d=json.load(sys.stdin)
    for k in sys.argv[1].split("."): d=d.get(k) if isinstance(d,dict) else None
    print("" if d is None else d)
except Exception:
    print("")' "$1" 2>/dev/null; }

# ---- the programs ------------------------------------------------------------------------------------------------------
cd "$root/coral-go" || exit 2
mkdir -p "$work/bin" "$work/pkg"
progs=$(grep -vE '^[[:space:]]*(#|$)' "$root/tests/release/commands.txt")
build_dir() { # VERSION DIR: the shipped programs and coral-update, built as installers/build-linux.sh builds them
  mkdir -p "$2"
  for cmd in $progs coral-update; do
    CGO_ENABLED=0 go build -tags fts5 -ldflags="-s -w -X github.com/cdknorow/coral/internal/config.Version=$1" -o "$2/$cmd" "./cmd/$cmd" || return 1
  done
}
pack() { tar czf "$2" -C "$1" .; } # as build-linux.sh makes its tarball
{
  build_dir "$N" "$work/pkg/n" && build_dir "$N1" "$work/pkg/n1" && build_dir "$NBAD" "$work/pkg/nbad" &&
  mkdir -p "$work/pkg/nunh" && cp "$work/pkg/n1"/* "$work/pkg/nunh"/ &&
  CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$N1" -o "$work/pkg/nunh/coral" ./internal/updater/testrig/cmd/unhealthy-coral &&
  CGO_ENABLED=0 go build -o "$work/bin/rehearsal-update" ./internal/updater/testrig/cmd/rehearsal-update &&
  pack "$work/pkg/n" "$work/pkg/n.tar.gz" && pack "$work/pkg/n1" "$work/pkg/n1.tar.gz" && pack "$work/pkg/nbad" "$work/pkg/nbad.tar.gz" && pack "$work/pkg/nunh" "$work/pkg/nunh.tar.gz"
} > "$work/build.log" 2>&1
if [ $? -ne 0 ]; then echo "update_rehearsal: the build failed:" >&2; tail -20 "$work/build.log" >&2; exit 2; fi

# ---- one scenario's world ----------------------------------------------------------------------------------------------
# a fresh install of N, a fresh HOME and data folder, the real server of N running, and a board message made through it
setup() { # NAME
  s="$work/$1"; inst="$s/install"; hh="$s/h"; dd="$s/d"
  mkdir -p "$inst" "$hh/.coral" "$dd"; echo accepted > "$hh/.coral/.eula-accepted"
  tar xzf "$work/pkg/n.tar.gz" -C "$inst" || return 1
  PORT=$(free_port)
  CENV=(env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$hh" CORAL_DATA_DIR="$dd" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 CORAL_NO_DEMO_SEED=1 CORAL_URL="http://127.0.0.1:$PORT")
  "${CENV[@]}" "$inst/coral" --home "$dd" --host 127.0.0.1 --port "$PORT" --backend pty --no-browser --cloud-url off > "$s/server.log" 2>&1 &
  srv=$!; PIDS+=("$srv")
  waitfor_health || return 1
  KEY=$(tr -d '[:space:]' < "$dd/api_key" 2>/dev/null)
  "${CENV[@]}" "$inst/coral-board" join rhsproj --as Tester > "$s/board.out" 2>&1
  "${CENV[@]}" "$inst/coral-board" post "a message before the update" >> "$s/board.out" 2>&1
  # The run record: how the server was started, which Linux apply reads to start Coral again after the swap. The server is documented to write it at
  # startup (internal/updater/runrecord) and NOTHING does yet (#5401 finding), so this script writes it, in the documented form: argv, cwd, 0600 in a 0700 folder.
  mkdir -p "$dd/updates" && chmod 700 "$dd/updates" &&
  ( umask 077; python3 - "$inst" "$dd" "$PORT" "$s" > "$dd/updates/run.json" <<'PY'
import json, sys
inst, dd, port, cwd = sys.argv[1:5]
print(json.dumps({"argv": [inst + "/coral", "--home", dd, "--host", "127.0.0.1", "--port", port, "--backend", "pty", "--no-browser", "--cloud-url", "off"], "cwd": cwd}))
PY
  ) && chmod 600 "$dd/updates/run.json"
}
health_version() { curl -s -m 3 "http://127.0.0.1:$PORT/api/health" | jget version; }
waitfor_health() { for _ in $(seq 1 150); do [ -n "$(health_version)" ] && return 0; sleep 0.2; done; return 1; }
waitfor_version() { # VERSION SECONDS
  for _ in $(seq 1 "$(( $2 * 5 ))"); do [ "$(health_version)" = "$1" ] && return 0; sleep 0.2; done; return 1
}
installed_version() { "${CENV[@]}" "$inst/coral" --version 2>&1 | awk '{print $2}'; }
message_back() { curl -s -m 5 -H "X-API-Key: $KEY" "http://127.0.0.1:$PORT/api/board/rhsproj/messages/all" | grep -c 'a message before the update'; }
count_servers() { c=0; for d in /proc/[0-9]*; do exe=$(readlink "$d/exe" 2>/dev/null) || continue; [ "$exe" = "$inst/coral" ] && c=$((c+1)); done; echo $c; }
run_rehearsal() { # PACKAGE VERSION [extra flags...]: runs it, waits for "ready", ends the server as the real server ends itself, waits for the end
  pkg=$1; ver=$2; shift 2
  "${CENV[@]}" "$work/bin/rehearsal-update" --data-dir "$dd" --install "$inst" --parent "$srv" --installed "$N" --package "$pkg" --version "$ver" "$@" > "$s/rehearsal.out" 2> "$s/rehearsal.err" &
  rpid=$!; PIDS+=("$rpid")
  for _ in $(seq 1 600); do
    grep -q '"step":"ready"' "$s/rehearsal.out" 2>/dev/null && break
    kill -0 "$rpid" 2>/dev/null || break
    sleep 0.2
  done
  if grep -q '"step":"ready"' "$s/rehearsal.out" 2>/dev/null; then kill -TERM "$srv" 2>/dev/null; wait "$srv" 2>/dev/null; fi
  for _ in $(seq 1 1500); do kill -0 "$rpid" 2>/dev/null || break; sleep 0.2; done
  wait "$rpid" 2>/dev/null
  rrc=$?
}
step_of() { python3 -c 'import sys,json
want=sys.argv[1]; out=""
for l in sys.stdin:
    try: d=json.loads(l)
    except Exception: continue
    if d.get("step")==want: out=d
print(json.dumps(out))' "$1" < "$s/rehearsal.out"; }

# ---- 1 update ----------------------------------------------------------------------------------------------------------
if setup update; then
  before_pid=$srv
  run_rehearsal "$work/pkg/n1.tar.gz" "$N1"
  ap=$(step_of apply | jget result)
  if waitfor_version "$N1" 90; then v=$N1; else v=$(health_version); fi
  alive=no; kill -0 "$before_pid" 2>/dev/null && alive=yes
  iv=$(installed_version); nsrv=$(count_servers); mb=$(message_back)
  if [ "$ap" = relaunched ] && [ "$v" = "$N1" ] && [ "$alive" = no ] && [ "$iv" = "$N1" ] && [ "$nsrv" = 1 ] && [ "$mb" -ge 1 ]; then
    result "1 update" PASS "$N -> $N1: apply relaunched; /api/health says $N1; the old process is gone; the install says $N1; one server runs; the board message is still there"
  else
    result "1 update" FAIL "apply=[$ap] health=[$v] old process alive=$alive install=[$iv] servers=$nsrv message back=$mb; rehearsal: $(tail -c 400 "$s/rehearsal.out" | tr '\n' ' ') $(tail -c 300 "$s/rehearsal.err" | tr '\n' ' ')"
  fi
else result "1 update" FAIL "the world of N did not come up: $(tail -3 "$work/update/server.log" 2>/dev/null | tr '\n' ' ' | cut -c1-300)"; fi
sweep

# ---- 2 tampered --------------------------------------------------------------------------------------------------------
if setup tampered; then
  pid0=$srv
  run_rehearsal "$work/pkg/n1.tar.gz" "$N1" --tamper bit
  ck=$(step_of check)
  cr=$(printf '%s' "$ck" | jget result); cs=$(printf '%s' "$ck" | jget failed_stage)
  alive=no; kill -0 "$pid0" 2>/dev/null && alive=yes
  v=$(health_version); iv=$(installed_version); staged=$(ls "$dd/updates/staging" 2>/dev/null | grep -c .)
  if [ "$cr" = failed ] && [ "$cs" = package ] && [ "$alive" = yes ] && [ "$v" = "$N" ] && [ "$iv" = "$N" ] && [ "$staged" = 0 ]; then
    result "2 tampered" PASS "a bit flipped on the wire: refused at the package stage, nothing staged, the server of $N untouched (same process, /api/health says $N)"
  else
    result "2 tampered" FAIL "check=[$cr/$cs] server alive=$alive health=[$v] install=[$iv] staged entries=$staged"
  fi
else result "2 tampered" FAIL "the world of N did not come up"; fi
sweep

# ---- 3 wrong -----------------------------------------------------------------------------------------------------------
if setup wrong; then
  sum_before=$(sha256sum "$inst/coral" | cut -d' ' -f1)
  run_rehearsal "$work/pkg/nbad.tar.gz" "$N1"
  ap=$(step_of apply); ar=$(printf '%s' "$ap" | jget result); as=$(printf '%s' "$ap" | jget failed_stage)
  iv=$(installed_version); sum_after=$(sha256sum "$inst/coral" | cut -d' ' -f1)
  siblings=$(ls "$s" | grep -c install)
  if [ "$ar" = refused ] && [ "$iv" = "$N" ] && [ "$sum_before" = "$sum_after" ]; then
    result "3 wrong" PASS "programs that say $NBAD in a release of $N1: apply $ar at [$as]; the install is byte for byte as it was ($iv)"
  else
    result "3 wrong" FAIL "apply=[$ar/$as] install=[$iv] same bytes=$([ "$sum_before" = "$sum_after" ] && echo yes || echo no); rehearsal: $(tail -c 300 "$s/rehearsal.out" | tr '\n' ' ')"
  fi
else result "3 wrong" FAIL "the world of N did not come up"; fi
sweep

# ---- 4 unhealthy -------------------------------------------------------------------------------------------------------
if setup unhealthy; then
  run_rehearsal "$work/pkg/nunh.tar.gz" "$N1" --health-for 15s
  ap=$(step_of apply); ar=$(printf '%s' "$ap" | jget result)
  iv=$(installed_version)
  if waitfor_version "$N" 60; then v=$N; else v=$(health_version); fi
  if [ "$ar" = failed_rolled_back ] && [ "$iv" = "$N" ] && [ "$v" = "$N" ]; then
    result "4 unhealthy" PASS "$N1 passed every check and did not stay up: rolled back; the install says $N and the server of $N answers /api/health again"
  else
    result "4 unhealthy" FAIL "apply=[$ar] install=[$iv] health=[$v]; rehearsal: $(tail -c 300 "$s/rehearsal.out" | tr '\n' ' ')"
  fi
else result "4 unhealthy" FAIL "the world of N did not come up"; fi
sweep

# ---- 5 crash -----------------------------------------------------------------------------------------------------------
if setup crash; then
  run_rehearsal "$work/pkg/n1.tar.gz" "$N1" --crash-after-swap --health-for 15s
  cr=$(step_of crashed); ap=$(step_of apply); ar=$(printf '%s' "$ap" | jget result)
  iv=$(installed_version)
  journal=no; [ -e "$dd/updates/apply-journal.json" ] && journal=yes
  # a server must start from whatever is installed, and say its version
  if [ "$(count_servers)" = 0 ]; then
    PORT=$(free_port)
    CENV=(env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$hh" CORAL_DATA_DIR="$dd" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 CORAL_NO_DEMO_SEED=1 CORAL_URL="http://127.0.0.1:$PORT")
    "${CENV[@]}" "$inst/coral" --home "$dd" --host 127.0.0.1 --port "$PORT" --backend pty --no-browser --cloud-url off > "$s/server2.log" 2>&1 &
    PIDS+=("$!")
  fi
  up=""; for _ in $(seq 1 150); do up=$(curl -s -m 3 "http://127.0.0.1:$PORT/api/health" | jget version); [ -n "$up" ] && break; sleep 0.2; done
  if [ -n "$cr" ] && { [ "$iv" = "$N" ] || [ "$iv" = "$N1" ]; } && [ "$journal" = no ] && [ -n "$up" ]; then
    result "5 crash" PASS "the apply died after the swap and the next one made the install consistent: it says $iv, no journal is left, and a server started from it says ${up} (apply: ${ar:-none})"
  else
    result "5 crash" FAIL "crashed step=[${cr:0:40}] install=[$iv] journal left=$journal server version=[$up]; rehearsal: $(tail -c 300 "$s/rehearsal.out" | tr '\n' ' ')"
  fi
else result "5 crash" FAIL "the world of N did not come up"; fi
sweep

# ---- the table ---------------------------------------------------------------------------------------------------------
echo
echo "update_rehearsal: $N -> $N1 on $(uname -m)"
fails=0
for i in "${!NAMES[@]}"; do
  printf '  %-12s %-5s %s\n' "${NAMES[$i]}" "${STATES[$i]}" "${DETAILS[$i]}"
  [ "${STATES[$i]}" = PASS ] || fails=$((fails + 1))
done
if [ "$fails" -ne 0 ]; then echo "update_rehearsal: FAIL ($fails item(s))"; exit 1; fi
echo "update_rehearsal: ok (all ${#NAMES[@]} items)"
