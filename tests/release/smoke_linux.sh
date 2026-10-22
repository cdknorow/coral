#!/usr/bin/env bash
# smoke_linux.sh: the release smoke of a Linux tarball, like smoke_macos.sh (#5306).
#
#   tests/release/smoke_linux.sh dist/coral-linux-amd64-1.4.130.tar.gz --version 1.4.130
#   tests/release/smoke_linux.sh coral-linux-arm64-1.4.130.tar.gz [--version V] [--keep]
#
# release.yml runs it on each architecture's own native runner right after the tarball is made, so the file that is published is the file that
# is tested. It unpacks the tarball into a fresh folder and runs the shipped binaries in a fresh HOME and data folder (never ~/.coral), on a random
# port, with CORAL_CLOUD_URL=off and a stand-in agent CLI (smoke_macos/standin_cli.py, shared with the macOS smoke) instead of a real one. Nothing is
# sent anywhere. Every process is stopped by the PID this script recorded; the folder is removed by its own path (--keep leaves it).
#
#   1 package    the tarball unpacks inside its folder (no absolute or .. names), holds every program of tests/release/commands.txt, each executable
#                and an ELF for this machine's architecture
#   2 version    coral --version prints the version given with --version (and, without it, a version that is not empty)
#   3 serve      the server answers /api/health and the dashboard page, takes the API key on the live list, and listens on 127.0.0.1 or ::1 only
#   4 launch     a launch with no agent type starts the stand-in CLI under the pty backend, the CLI's own hooks make the agent "waiting: question",
#                the question is answered through the API (toggles read back from its terminal, Submit) and the CLI received exactly the boxes ticked
#   5 stop       SIGTERM stops the server within 20 s, with exit status 0 or 143, its port is closed, and no agent process it started is left
#
# Output: one table; exit status 0 only when no item FAILs, the failing item is named on stderr. Needs Linux, bash, curl, python3 and tar.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tarball="" want_version="" keep=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) want_version=${2:?--version V}; shift 2 ;;
    --keep) keep=1; shift ;;
    -h|--help) sed -n 2,26p "$0"; exit 0 ;;
    -*) echo "smoke_linux: unknown option $1" >&2; exit 2 ;;
    *) tarball=$1; shift ;;
  esac
done
[ "$(uname -s)" = Linux ] || { echo "smoke_linux: Linux only" >&2; exit 2; }
for t in curl python3 tar; do command -v "$t" >/dev/null || { echo "smoke_linux: needs $t" >&2; exit 2; }; done
[ -n "$tarball" ] && [ -f "$tarball" ] || { echo "usage: smoke_linux.sh TARBALL [--version V] [--keep]" >&2; exit 2; }
tarball=$(cd "$(dirname "$tarball")" && pwd)/$(basename "$tarball")

work=$(mktemp -d /tmp/coral-linsmoke.XXXXXX) || exit 2   # short path: sockets have a length limit
PIDS=()
cleanup() {
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  sleep 1
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill -0 "$p" 2>/dev/null && kill -9 "$p" 2>/dev/null; done
  if [ "$keep" = 1 ]; then echo "smoke_linux: kept $work"; else case "$work" in /tmp/coral-linsmoke.??????) rm -rf -- "$work" ;; esac; fi
}
trap cleanup EXIT

NAMES=() STATES=() DETAILS=()
result() { NAMES+=("$1"); STATES+=("$2"); DETAILS+=("$3"); [ "$2" = PASS ] || echo "smoke_linux: $2 $1: $3" >&2; }
jget() { python3 -c 'import sys,json
d=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d = d[int(k)] if isinstance(d, list) else d.get(k)
print(d)' "$1" 2>/dev/null; }
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }

# ---- 1 package ----------------------------------------------------------------------------------------------
pkg="$work/pkg"; mkdir -p "$pkg"
item1=""
bad=$(tar -tzf "$tarball" 2>/dev/null | grep -E '(^/|(^|/)\.\.(/|$))' | head -1)
[ -z "$bad" ] || item1="the tarball names a file outside its folder: $bad"
[ -n "$item1" ] || tar -xzf "$tarball" -C "$pkg" 2>/dev/null || item1="the tarball does not unpack"
if [ -z "$item1" ]; then
  cmds=$(grep -vE '^[[:space:]]*(#|$)' "$here/commands.txt")
  machine=$(uname -m)
  missing=""
  for c in $cmds; do
    f="$pkg/$c"
    if [ ! -f "$f" ] || [ ! -x "$f" ]; then missing="$missing $c(missing or not executable)"; continue; fi
    elf=$(python3 - "$f" "$machine" <<'PY'
import struct, sys
f, machine = sys.argv[1], sys.argv[2]
want = {"x86_64": 62, "aarch64": 183}.get(machine)
h = open(f, "rb").read(20)
if h[:4] != b"\x7fELF": print("not an ELF")
elif want is None: print("this machine's architecture is not amd64 or arm64: " + machine)
else:
    e = struct.unpack("<H", h[18:20])[0]
    print("ok" if e == want else "an ELF for machine %d, this machine is %s" % (e, machine))
PY
)
    [ "$elf" = ok ] || missing="$missing $c($elf)"
  done
  [ -z "$missing" ] || item1="programs:$missing"
fi
if [ -z "$item1" ]; then result "1 package" PASS "unpacks inside its folder; $(echo $cmds | wc -w | tr -d ' ') programs present, executable, ELF for $(uname -m)"
else result "1 package" FAIL "$item1"; fi

run_ok=1; [ -z "$item1" ] || run_ok=0
hh="$work/h"; dd="$work/d"; mkdir -p "$hh/.coral" "$dd" "$hh/.claude/projects" "$work/wd"; echo accepted > "$hh/.coral/.eula-accepted"
# an array, not a function: a backgrounded function is a subshell, and $! would be the subshell, not coral (a SIGTERM to it left the server running)
CENV=(env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$hh" CORAL_DATA_DIR="$dd" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 CORAL_NO_DEMO_SEED=1)

# ---- 7 updater: coral-update ships in the package and is inert: its status says the apply is off (applyEnabled is a compile-time false until a platform passes its upgrade tests) ----
if [ "$run_ok" = 1 ]; then
  st=$("${CENV[@]}" "$pkg/coral-update" status --json 2>&1)
  if grep -Eq '"apply_enabled": *false' <<<"$st"; then result "7 updater" PASS "coral-update ships, status says apply_enabled false"
  else result "7 updater" FAIL "coral-update status: $(head -c 200 <<<"$st" | tr '\n' ' ')"; fi
fi

# ---- 2 version ----------------------------------------------------------------------------------------------
if [ "$run_ok" = 1 ]; then
  out=$("${CENV[@]}" "$pkg/coral" --version 2>&1 | head -3)
  if [ -n "$want_version" ]; then
    if printf '%s' "$out" | grep -qF -- "$want_version"; then result "2 version" PASS "coral --version: $(echo "$out" | head -1)"
    else result "2 version" FAIL "coral --version printed [$(echo "$out" | head -1)], not $want_version"; fi
  else
    if [ -n "$out" ]; then result "2 version" PASS "coral --version: $(echo "$out" | head -1) (no --version given to compare)"
    else result "2 version" FAIL "coral --version printed nothing"; fi
  fi
else result "2 version" FAIL "no package to run (item 1)"; fi

# ---- 3 serve ------------------------------------------------------------------------------------------------
srv="" PORT="" KEY=""
if [ "$run_ok" = 1 ]; then
  PORT=$(free_port)
  "${CENV[@]}" "$pkg/coral" --home "$dd" --host 127.0.0.1 --port "$PORT" --backend pty --no-browser --cloud-url off > "$work/server.log" 2>&1 &
  srv=$!; PIDS+=("$srv")
  for _ in $(seq 1 150); do
    curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1 && break
    kill -0 "$srv" 2>/dev/null || break
    sleep 0.2
  done
  health=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "http://127.0.0.1:$PORT/api/health")
  KEY=$(tr -d '[:space:]' < "$dd/api_key" 2>/dev/null)
  page=$(curl -sL -m 10 -o "$work/page.html" -w '%{http_code}' "http://127.0.0.1:$PORT/")   # / redirects to /agents
  live=$(curl -s -o /dev/null -w '%{http_code}' -m 10 -H "Authorization: Bearer $KEY" "http://127.0.0.1:$PORT/api/sessions/live")
  noauth=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "http://127.0.0.1:$PORT/api/sessions/live")
  listen=$(python3 - "$PORT" <<'PY'
import sys
port = int(sys.argv[1]); out = set()
for f, loop in (("/proc/net/tcp", {"0100007F"}), ("/proc/net/tcp6", {"00000000000000000000000001000000", "0000000000000000FFFF00000100007F"})):
    try: lines = open(f).read().splitlines()[1:]
    except OSError: continue
    for l in lines:
        p = l.split()
        if p[3] != "0A": continue
        addr, prt = p[1].split(":")
        if int(prt, 16) == port: out.add(("loopback" if addr in loop else addr) )
print(" ".join(sorted(out)) or "none")
PY
)
  if [ "$health" = 200 ] && [ "$page" = 200 ] && grep -qi "<title>[^<]*coral" "$work/page.html" && [ "$live" = 200 ] && [ "$noauth" = 401 ] && [ "$listen" = loopback ]; then
    result "3 serve" PASS "health 200, the dashboard page 200 (Coral), the live list 200 with the key and 401 without, listening on loopback only"
  else
    result "3 serve" FAIL "health=$health page=$page (title Coral: $(grep -ci '<title>[^<]*coral' "$work/page.html")) live=$live without a credential=$noauth listening=[$listen]; server log tail: $(tail -3 "$work/server.log" | tr '\n' '|' | cut -c1-300)"
  fi
else result "3 serve" FAIL "no package to run (item 1)"; fi
served=0; [ -n "$srv" ] && [ "${STATES[2]:-}" = PASS ] && served=1
api() { curl -s -m 30 -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@"; }

# ---- 4 launch -----------------------------------------------------------------------------------------------
agent_pid=""
if [ "$served" = 1 ]; then
  api -X PUT -d "{\"cli_path_claude\":\"$here/smoke_macos/standin_cli.py\"}" "http://127.0.0.1:$PORT/api/settings" -o /dev/null
  launch=$(api -X POST -d "{\"working_dir\":\"$work/wd\",\"display_name\":\"smoke\"}" "http://127.0.0.1:$PORT/api/sessions/launch")
  sid=$(printf '%s' "$launch" | jget session_id); atype=$(printf '%s' "$launch" | jget agent_type); name="claude-$sid"; kind=""
  for _ in $(seq 1 80); do
    kind=$(api "http://127.0.0.1:$PORT/api/sessions/live" | python3 -c 'import sys,json
d=json.load(sys.stdin); l=d if isinstance(d,list) else d.get("sessions",[])
print(next((s.get("waiting_kind") or "" for s in l if s.get("session_id")==sys.argv[1]), ""))' "$sid" 2>/dev/null)
    [ "$kind" = question ] && break; sleep 0.5
  done
  ans() { api -X POST -d "$1" -w '\n%{http_code}' "http://127.0.0.1:$PORT/api/sessions/live/$name/answer-prompt"; }
  body() { printf '%s' "$1" | sed '$d'; }
  code() { printf '%s' "$1" | tail -1; }
  flag() { body "$1" | jget checked; }
  base="\"session_id\":\"$sid\",\"agent_type\":\"claude\""
  t1=$(ans "{$base,\"n\":2,\"label\":\"Doesn't show the game\"}"); t2=$(ans "{$base,\"n\":3,\"label\":\"Tone/story is off\"}")
  t3=$(ans "{$base,\"n\":3,\"label\":\"Tone/story is off\"}"); t4=$(ans "{$base,\"n\":4,\"label\":\"Looks cheap/amateur\"}")
  sub=$(ans "{$base,\"submit\":true}")
  for _ in $(seq 1 20); do [ -s "$work/wd/standin-answer.txt" ] && break; sleep 0.5; done
  got=$(cat "$work/wd/standin-answer.txt" 2>/dev/null)
  # the agent's own process, by the session id on its command line (recorded for the stop check and the cleanup)
  for d in /proc/[0-9]*; do
    if tr '\0' ' ' < "$d/cmdline" 2>/dev/null | grep -qF -- "--session-id $sid"; then agent_pid=${d#/proc/}; PIDS+=("$agent_pid"); break; fi
  done
  if [ "$atype" = claude ] && [ "$kind" = question ] && [ "$(flag "$t1")" = True ] && [ "$(flag "$t2")" = True ] && [ "$(flag "$t3")" = False ] && [ "$(flag "$t4")" = True ] \
     && [ "$(code "$sub")" = 200 ] && body "$sub" | grep -q '"ok":true' && [ "$got" = '["Doesn'"'"'t show the game", "Looks cheap/amateur"]' ]; then
    result "4 launch" PASS "a launch with no type started claude (the stand-in) under pty; its hooks made it waiting_kind=question; toggles read back from its terminal; Submit ok; it received exactly boxes 2 and 4"
  else
    result "4 launch" FAIL "agent_type=[$atype] waiting_kind=[$kind] toggles=[$(flag "$t1") $(flag "$t2") $(flag "$t3") $(flag "$t4")] submit=[$(code "$sub") $(body "$sub" | cut -c1-80)] the CLI got [$got] hook errors: $(head -1 "$work/wd/standin-hook-error.txt" 2>/dev/null)"
  fi
else result "4 launch" FAIL "no running server (item 3)"; fi

# ---- 5 stop -------------------------------------------------------------------------------------------------
if [ -n "$srv" ] && kill -0 "$srv" 2>/dev/null; then
  kill -TERM "$srv" 2>/dev/null
  rc=""; for _ in $(seq 1 100); do
    if ! kill -0 "$srv" 2>/dev/null; then wait "$srv" 2>/dev/null; rc=$?; break; fi
    sleep 0.2
  done
  if [ -z "$rc" ]; then
    result "5 stop" FAIL "the server was still running 20 s after SIGTERM"
  else
    still=""
    # alive: running, not a zombie (a dead process nobody has reaped yet still answers kill -0; a container's PID 1 does not reap)
    alive() { kill -0 "$1" 2>/dev/null && [ "$(awk '{print $3}' "/proc/$1/stat" 2>/dev/null)" != Z ]; }
    if [ -n "$agent_pid" ]; then   # the server's shutdown kills the pty children (startup.Shutdown, ptymanager Close): give them 5 s to go
      for _ in $(seq 1 25); do alive "$agent_pid" || break; sleep 0.2; done
      alive "$agent_pid" && still="the agent process $agent_pid is still running 5 s after the server exited"
    fi
    closed=$(curl -s -o /dev/null -w '%{http_code}' -m 3 "http://127.0.0.1:$PORT/api/health"); [ "$closed" = 000 ] || still="$still; port $PORT still answers ($closed)"
    if { [ "$rc" = 0 ] || [ "$rc" = 143 ]; } && [ -z "$still" ]; then result "5 stop" PASS "SIGTERM: exit status $rc, port closed, no agent process left"
    else result "5 stop" FAIL "exit status $rc; ${still:-nothing left running}"; fi
  fi
else result "5 stop" FAIL "no running server to stop"; fi

# ---- the table ----------------------------------------------------------------------------------------------
echo
echo "smoke_linux: $(basename "$tarball")"
fails=0
for i in "${!NAMES[@]}"; do
  printf '  %-12s %-5s %s\n' "${NAMES[$i]}" "${STATES[$i]}" "${DETAILS[$i]}"
  [ "${STATES[$i]}" = PASS ] || fails=$((fails + 1))
done
if [ "$fails" -ne 0 ]; then echo "smoke_linux: FAIL ($fails item(s))"; exit 1; fi
echo "smoke_linux: ok (all ${#NAMES[@]} items)"
