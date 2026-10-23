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
#   6 ask         the same swap, started the way a person starts it (#6297): the real server of N with a stand-in coral-update beside it (status passes through to the real one, apply
#                 runs the rehearsal program), a staged update, and the server's relaunch control. Not the person (an API key, an agent's token, no credential) is refused and nothing
#                 starts; the person (a request signed with the data folder's command-line token) starts it; the server ends, N+1 answers /api/health, the board message is kept, a pty agent (the mock agent) listed before comes back as a new process
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
    CGO_ENABLED=0 go build -tags fts5 -ldflags="-s -w -X github.com/subgentic/coral/internal/config.Version=$1" -o "$2/$cmd" "./cmd/$cmd" || return 1
  done
}
pack() { tar czf "$2" -C "$1" .; } # as build-linux.sh makes its tarball
{
  build_dir "$N" "$work/pkg/n" && build_dir "$N1" "$work/pkg/n1" && build_dir "$NBAD" "$work/pkg/nbad" &&
  mkdir -p "$work/pkg/nunh" && cp "$work/pkg/n1"/* "$work/pkg/nunh"/ &&
  CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$N1" -o "$work/pkg/nunh/coral" ./internal/updater/testrig/cmd/unhealthy-coral &&
  CGO_ENABLED=0 go build -o "$work/bin/rehearsal-update" ./internal/updater/testrig/cmd/rehearsal-update &&
  CGO_ENABLED=0 go build -o "$work/bin/person-call" ./internal/updater/testrig/cmd/person-call &&
  CGO_ENABLED=0 go build -o "$work/bin/mock-agent" ./cmd/mock-agent &&
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
  # The run record: how the server was started, which the Linux apply reads to start Coral again after the swap. The server writes it itself at startup (cmd/coral/run_record.go,
  # #5339) with its own environment; an earlier version of this script wrote one by hand, without an environment, which the apply now refuses (runrecord.Validate, #5419).
  # RHS_NO_RUN_RECORD=1 removes it: the diagnostic that shows what an apply does without it (it refuses)
  for _ in $(seq 1 50); do [ -s "$dd/updates/run.json" ] && break; sleep 0.2; done
  [ -s "$dd/updates/run.json" ] || { echo "update_rehearsal: the server did not write its run record" >&2; return 1; }
  [ -n "${RHS_NO_RUN_RECORD:-}" ] && rm -f "$dd/updates/run.json"
  return 0
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
# The refusal must be for the tampered bytes: the check names the digest mismatch (the updater's own words), the package really was asked for, and a package that cannot be downloaded at all
# (the control, in a data folder of its own) is a DIFFERENT result that this item rejects. Without that, a rig that was down would pass or fail this item by luck (#6297, Security).
if setup tampered; then
  pid0=$srv
  run_rehearsal "$work/pkg/n1.tar.gz" "$N1" --tamper bit
  ck=$(step_of check)
  cr=$(printf '%s' "$ck" | jget result); cs=$(printf '%s' "$ck" | jget failed_stage)
  why=$(printf '%s' "$ck" | jget reason); hits=$(printf '%s' "$ck" | jget package_requests)
  mkdir -p "$s/ctl"
  "${CENV[@]}" "$work/bin/rehearsal-update" --data-dir "$s/ctl" --install "$inst" --parent "$srv" --installed "$N" --package "$work/pkg/n1.tar.gz" --version "$N1" --tamper break --mode check > "$s/ctl.out" 2> "$s/ctl.err"
  ctl=$(grep '"step":"check"' "$s/ctl.out" | tail -1)
  ctl_stage=$(printf '%s' "$ctl" | jget failed_stage); ctl_reason=$(printf '%s' "$ctl" | jget reason)
  alive=no; kill -0 "$pid0" 2>/dev/null && alive=yes
  v=$(health_version); iv=$(installed_version); staged=$(ls "$dd/updates/staging" 2>/dev/null | grep -c .)
  digest=no; case "$why" in *"the verified sums file says"*) digest=yes ;; esac
  if [ "$cr" = failed ] && [ "$cs" = package ] && [ "$digest" = yes ] && [ "${hits:-0}" -ge 1 ] && [ "$ctl_stage" = download ] && [ -z "$ctl_reason" ] && [ "$alive" = yes ] && [ "$v" = "$N" ] && [ "$iv" = "$N" ] && [ "$staged" = 0 ]; then
    result "2 tampered" PASS "a bit flipped on the wire: refused at the package stage with the digest mismatch named, the package asked for $hits time(s), nothing staged, the server of $N untouched (same process, /api/health says $N); the control (a package that 404s) is a $ctl_stage failure with no digest reason, a different result"
  else
    result "2 tampered" FAIL "check=[$cr/$cs] reason=[${why:0:160}] package requests=[$hits] control=[$ctl_stage/${ctl_reason:0:60}] server alive=$alive health=[$v] install=[$iv] staged entries=$staged; rehearsal err: $(tail -c 300 "$s/rehearsal.err" | tr '\n' ' ')"
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

# ---- 6 ask -------------------------------------------------------------------------------------------------------------
# The relaunch control of the real server (#6290, #6297). The stand-in coral-update is the only thing that is not the real program: "status" is the real coral-update's, "apply" is the rehearsal's
# (the fake world's keys are made when it starts, so it stages again, --restage, and applies in one run), "check" says up to date. The person is person-call: a request signed with the data
# folder's command-line token, the credential the server takes for a person besides the dashboard's cookie.
if setup ask; then
  pid0=$srv
  # the update is staged by an earlier run of the rehearsal program, which then ends: what the server's status shows is the real updater's reading of that state
  "${CENV[@]}" "$work/bin/rehearsal-update" --data-dir "$dd" --install "$inst" --parent "$srv" --installed "$N" --package "$work/pkg/n1.tar.gz" --version "$N1" --mode check > "$s/stage.out" 2> "$s/stage.err"
  # The status is the real coral-update's, run from a copy of the install (it describes the folder it runs in, and a folder holding a renamed or extra file is "shared_folder", not a
  # supported install); the install itself keeps only the stand-in, named coral-update as in a real one.
  cp -a "$inst" "$s/statusinst"
  cat > "$inst/coral-update" <<SHIM
#!/bin/sh
# the ask rehearsal's stand-in for coral-update (tests/release/update_rehearsal.sh): never released
case "\$1" in
  status) exec "$s/statusinst/coral-update" "\$@" ;;
  check) echo '{"result":"up_to_date"}'; exit 0 ;;
  apply) exec env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$hh" CORAL_DATA_DIR="$dd" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 CORAL_NO_DEMO_SEED=1 CORAL_URL="http://127.0.0.1:$PORT" "$work/bin/rehearsal-update" --data-dir "$dd" --install "$inst" --parent "$srv" --installed "$N" --package "$work/pkg/n1.tar.gz" --version "$N1" --mode both --restage ;;
esac
exit 2
SHIM
  chmod 755 "$inst/coral-update"
  base="http://127.0.0.1:$PORT"
  person() { "${CENV[@]}" "$work/bin/person-call" --data-dir "$dd" --url "$base" "$@"; } # PERSON args: --method M --path P [--body B]: prints "CODE body"
  # an agent on a pty (the mock agent, as the CLI), so that "the agents come back" is looked at: its process before and after
  agent_pids() { for d in /proc/[0-9]*; do exe=$(readlink "$d/exe" 2>/dev/null) || continue; [ "$exe" = "$work/bin/mock-agent" ] && echo "${d#/proc/}"; done | sort -n | tr '\n' ' '; }
  person --method PUT --path /api/settings --body "{\"cli_path_claude\":\"$work/bin/mock-agent\"}" > "$s/cli.out"
  mkdir -p "$s/agentdir"
  person --method POST --path /api/sessions/launch --body "{\"working_dir\":\"$s/agentdir\",\"agent_type\":\"claude\",\"backend\":\"pty\",\"board_id\":\"rhsproj\",\"display_name\":\"Rig\"}" > "$s/launch.out"
  for _ in $(seq 1 50); do [ -n "$(agent_pids)" ] && break; sleep 0.2; done
  agents_before=$(agent_pids)
  st=$(person --path /api/system/update | cut -d' ' -f2-)
  staged=$(printf '%s' "$st" | jget staged.version); source_=$(printf '%s' "$st" | jget staged.source)
  avail=$(printf '%s' "$st" | jget relaunch.available); hub_allowed=$(printf '%s' "$st" | jget relaunch.hub_allowed)
  tok=$(printf '%s' "$st" | jget relaunch.ack); reason=$(printf '%s' "$st" | jget relaunch.reason)
  ok=1; why=""
  restarts=$(printf '%s' "$st" | jget relaunch.restarts)
  { [ -n "$agents_before" ] && [ -n "$restarts" ] && [ "$restarts" != "[]" ]; } || { ok=0; why="$why the agent is not running or not listed (pids=[$agents_before] restarts=[$restarts]); launch: $(cut -c1-120 "$s/launch.out");"; }
  [ "$staged" = "$N1" ] || { ok=0; why="$why staged=[$staged] reason=[$reason];"; }
  [ "$source_" = "subgentic/coral" ] || { ok=0; why="$why source=[$source_];"; }
  [ "$avail" = True ] || [ "$avail" = true ] || { ok=0; why="$why available=[$avail];"; }
  [ -n "$tok" ] || { ok=0; why="$why no ack;"; }
  [ "$hub_allowed" = False ] || [ "$hub_allowed" = false ] || { ok=0; why="$why hub_allowed=[$hub_allowed] (the Hub switch is off by default);"; }
  body="{\"ack\":\"$tok\"}"
  # not a person: an API key (the key is not a person's yes), an agent's session token, no credential at all, the Hub's own route used by the person
  c_key=$(curl -s -m 10 -o /dev/null -w '%{http_code}' -X POST -H "X-API-Key: $KEY" -H 'Content-Type: application/json' -d "$body" "$base/api/system/update/relaunch-local")
  c_agent=$(curl -s -m 10 -o /dev/null -w '%{http_code}' -X POST -H "X-Coral-Session-Token: not-a-token" -H 'Content-Type: application/json' -d "$body" "$base/api/system/update/relaunch-local")
  c_none=$(curl -s -m 10 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "$body" "$base/api/system/update/relaunch-local")
  c_hub=$(person --method POST --path /api/system/update/relaunch --body "$body" | cut -d' ' -f1)
  [ "$c_key" = 403 ] || { ok=0; why="$why API key got $c_key;"; }
  case "$c_agent" in 401|403) ;; *) ok=0; why="$why agent token got $c_agent;" ;; esac
  case "$c_none" in 401|403) ;; *) ok=0; why="$why no credential got $c_none;" ;; esac
  case "$c_hub" in 403|404) ;; *) ok=0; why="$why the Hub's route for the person got $c_hub;" ;; esac
  sleep 2
  v=$(health_version); alive=no; kill -0 "$pid0" 2>/dev/null && alive=yes
  started=$(ls "$dd/updates" 2>/dev/null | grep -c 'ready\|apply-journal')
  [ "$v" = "$N" ] && [ "$alive" = yes ] || { ok=0; why="$why after the refusals health=[$v] server alive=$alive;"; }
  [ "$started" = 0 ] || { ok=0; why="$why an apply started without a person;"; }
  # the person
  pr=$(person --method POST --path /api/system/update/relaunch-local --body "$body"); pcode=${pr%% *}
  [ "$pcode" = 202 ] || { ok=0; why="$why the person got [$pr];"; }
  if waitfor_version "$N1" 90; then v=$N1; else v=$(health_version); fi
  alive=no; kill -0 "$pid0" 2>/dev/null && alive=yes
  iv=$(installed_version); nsrv=$(count_servers); mb=$(message_back)
  [ "$v" = "$N1" ] && [ "$alive" = no ] && [ "$iv" = "$N1" ] && [ "$nsrv" = 1 ] && [ "$mb" -ge 1 ] || { ok=0; why="$why after the yes health=[$v] old alive=$alive install=[$iv] servers=$nsrv message back=$mb;"; }
  # the agent comes back: a new process of the mock agent, not the old one (the old server ended it with the swap)
  agents_after=""; for _ in $(seq 1 150); do agents_after=$(agent_pids); [ -n "$agents_after" ] && [ "$agents_after" != "$agents_before" ] && break; sleep 0.4; done
  { [ -n "$agents_after" ] && [ "$agents_after" != "$agents_before" ]; } || { ok=0; why="$why the agent did not come back (pids before=[$agents_before] after=[$agents_after]);"; }
  if [ "$ok" = 1 ]; then
    result "6 ask" PASS "staged $N1 (source subgentic/coral, Hub switch off); an API key, an agent token, no credential and the Hub's route got 403/401/401/403 and started nothing; the person's signed request got 202 and the server of $N was replaced by $N1 (board message kept, one server); the pty agent listed before came back as a new process (pids [$agents_before] then [$agents_after])"
  else
    result "6 ask" FAIL "$why status: $(printf '%s' "$st" | cut -c1-300); stage: $(tail -c 200 "$s/stage.out" | tr '\n' ' ') $(tail -c 200 "$s/stage.err" | tr '\n' ' ')"
  fi
else result "6 ask" FAIL "the world of N did not come up"; fi
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
