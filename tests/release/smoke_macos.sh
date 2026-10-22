#!/usr/bin/env bash
# smoke_macos.sh: the post-release smoke of the macOS build (QA's #5253 as a script, #5256).
#
#   tests/release/smoke_macos.sh v1.4.123            # downloads the DMG and SHA256SUMS.txt of the release with gh
#   tests/release/smoke_macos.sh --dmg Coral.v1.4.123.dmg --sums SHA256SUMS.txt [--keep]
#
# Run it after tools/verify-release.sh. It takes the DMG's app and runs the RELEASE binaries (never the installed
# app, never ~/.coral) in a fresh HOME and data folder, on random ports, with CORAL_CLOUD_URL=off and a stand-in
# agent CLI instead of a real one. Everything the tray and the server send over HTTP(S) goes through a local proxy
# that refuses it and logs it, so nothing leaves the machine. Every process is stopped by the PID this script
# recorded; the folder is removed by its own path (--keep leaves it, for a look).
#
#   1 dmg        the DMG's sha256 is the one in SHA256SUMS.txt; the app is codesign-valid and Notarized Developer ID
#   2 loopback   the tray starts the server, and it listens on 127.0.0.1 only (no other interface, privacy says so)
#   3 analytics  a control run with default settings is seen reaching the analytics host (so the proxy can see it),
#                then with telemetry_enabled=false saved first the tray and the server never reach it
#   4 comments   an artifact comment is saved with its quote and the owner as author, and is still there on the next read
#   5 multiselect  the "Claude is asking" question of the stand-in CLI: toggles read back from its terminal, a stale label
#                is refused, Submit reaches the CLI, and the CLI got exactly the boxes that were ticked
#   6 settings   an operator-shaped settings set written by older versions (JSON-style True, legacy keys) is read:
#                privacy, launch defaults, the legacy key removed, the values kept as written
#
# The dashboard's own rendering of the card and the comments panel is not driven here (that needs a browser):
# tests/frontend/artifact_comments_server_browser.test.js and the CDP suites cover it; this smoke covers the
# released binaries and the server's side of both.
#
# Output: one table. Exit status 0 only when no item FAILs; the failing item is named on stderr. Needs macOS,
# curl, sqlite3, lsof, python3, hdiutil, codesign and spctl (gh for a TAG).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
. "$here/../../tools/release-homes.sh"
dmg="" sums="" tag="" keep=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dmg) dmg=${2:?--dmg FILE}; shift 2 ;;
    --sums) sums=${2:?--sums FILE}; shift 2 ;;
    --keep) keep=1; shift ;;
    -h|--help) sed -n 2,30p "$0"; exit 0 ;;
    -*) echo "smoke_macos: unknown option $1" >&2; exit 2 ;;
    *) tag=$1; shift ;;
  esac
done
[ "$(uname -s)" = Darwin ] || { echo "smoke_macos: macOS only" >&2; exit 2; }
for t in curl sqlite3 lsof python3 hdiutil codesign spctl shasum; do command -v "$t" >/dev/null || { echo "smoke_macos: needs $t" >&2; exit 2; }; done
[ -n "$dmg" ] || [ -n "$tag" ] || { echo "usage: smoke_macos.sh TAG | --dmg FILE [--sums FILE] [--keep]" >&2; exit 2; }

work=$(mktemp -d /tmp/coral-macsmoke.XXXXXX) || exit 2   # short path: tmux/pty sockets have a length limit
PIDS=()
mounted=0
cleanup() {
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  sleep 1
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill -0 "$p" 2>/dev/null && kill -9 "$p" 2>/dev/null; done
  [ "$mounted" = 1 ] && hdiutil detach "$work/mnt" -quiet 2>/dev/null
  if [ "$keep" = 1 ]; then echo "smoke_macos: kept $work"; else case "$work" in /tmp/coral-macsmoke.??????) rm -rf -- "$work" ;; esac; fi
}
trap cleanup EXIT

NAMES=() STATES=() DETAILS=()
result() { NAMES+=("$1"); STATES+=("$2"); DETAILS+=("$3"); [ "$2" = PASS ] || echo "smoke_macos: $2 $1: $3" >&2; }
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
jget() { python3 -c 'import sys,json
d=json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d = d[int(k)] if isinstance(d, list) else d.get(k)
print(d)' "$1" 2>/dev/null; }

# ---- 1 dmg -------------------------------------------------------------------------------------------------
app=""
if [ -z "$dmg" ]; then
  command -v gh >/dev/null || { echo "smoke_macos: a TAG needs gh" >&2; exit 2; }
  gh release download "$tag" -R "$(release_home_current)" -p 'Coral*.dmg' -p SHA256SUMS.txt -D "$work/dl" >/dev/null 2>&1 || { echo "smoke_macos: could not download $tag" >&2; exit 2; }
  dmg=$(ls "$work"/dl/Coral*.dmg | head -1); sums="$work/dl/SHA256SUMS.txt"
fi
[ -f "$dmg" ] || { echo "smoke_macos: no DMG at $dmg" >&2; exit 2; }
item1=""
if [ -n "$sums" ]; then
  want=$(awk -v f="$(basename "$dmg")" '$2==f || $2=="*"f {print $1}' "$sums" | head -1)
  got=$(shasum -a 256 "$dmg" | awk '{print $1}')
  if [ -z "$want" ]; then item1="$(basename "$dmg") is not listed in $sums"; elif [ "$want" != "$got" ]; then item1="sha256 $got is not the published $want"; fi
else
  item1="no SHA256SUMS.txt given (--sums), the digest was not checked"
fi
if [ -z "$item1" ]; then
  mkdir -p "$work/mnt"
  if hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$work/mnt" "$dmg" >/dev/null 2>&1; then
    mounted=1
    ditto "$work/mnt/Coral.app" "$work/Coral.app" 2>/dev/null || item1="the DMG has no Coral.app"
    hdiutil detach "$work/mnt" -quiet 2>/dev/null && mounted=0
  else
    item1="the DMG does not mount"
  fi
fi
if [ -z "$item1" ]; then
  codesign --verify --deep --strict "$work/Coral.app" 2>/dev/null || item1="codesign --verify --deep --strict fails"
  [ -n "$item1" ] || spctl -a -vv "$work/Coral.app" > "$work/spctl.txt" 2>&1
  [ -n "$item1" ] || { grep -q "accepted" "$work/spctl.txt" && grep -q "Notarized Developer ID" "$work/spctl.txt" || item1="spctl does not say accepted, Notarized Developer ID: $(head -2 "$work/spctl.txt" | tr '\n' ' ')"; }
fi
if [ -z "$item1" ]; then
  result "1 dmg" PASS "sha256 matches, codesign valid, $(grep -o 'source=.*' "$work/spctl.txt" | head -1)"
  app="$work/Coral.app/Contents/MacOS"
else
  result "1 dmg" FAIL "$item1"
fi
# 7 updater: coral-update ships in the app and is inert: its status says the apply is off (applyEnabled is a compile-time false until a platform passes its upgrade tests)
if [ -n "$app" ] && [ -x "$app/coral-update" ]; then
  ud="$(mktemp -d "$work/ud.XXXX")"
  st="$(env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$ud" CORAL_DATA_DIR="$ud/data" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 "$app/coral-update" status --json 2>&1)"
  if grep -Eq '"apply_enabled": *false' <<<"$st"; then result "7 updater" PASS "coral-update ships, status says apply_enabled false"; else result "7 updater" FAIL "coral-update status: $(head -c 200 <<<"$st" | tr '\n' ' ')"; fi
elif [ -n "$app" ]; then result "7 updater" FAIL "coral-update is not in the app"; fi
# the rest needs the app: say so, per item, rather than skipping quietly
if [ -z "$app" ] || [ ! -x "$app/coral-tray" ] || [ ! -x "$app/coral" ]; then
  for i in "2 loopback" "3 analytics" "4 comments" "5 multiselect" "6 settings"; do result "$i" FAIL "no app to run (item 1)"; done
  app=""
fi

# ---- one running tray: start_tray NAME SEED.sql|none  ->  sets TRAY_* ------------------------------------------
TRAY_PID="" SRV_PID="" PROXY_PID="" PORT="" KEY="" DD="" HH="" PLOG=""
start_tray() {
  local n=$1 seed=$2 pp p0 sp i
  HH="$work/h/$n"; DD="$work/d/$n"; PLOG="$work/$n.proxy.log"
  mkdir -p "$HH/.coral" "$DD" "$HH/.claude/projects" && echo accepted > "$HH/.coral/.eula-accepted"
  : > "$PLOG"; pp=$(free_port)
  python3 "$here/smoke_macos/refusing_proxy.py" "$pp" "$PLOG" & PROXY_PID=$!; PIDS+=("$PROXY_PID"); disown "$PROXY_PID"
  for i in $(seq 1 40); do [ -e "$PLOG.up" ] && break; sleep 0.25; done
  if [ "$seed" != none ]; then
    # the schema is made by a first start of the release server (no proxy, telemetry off), stopped before the set is written
    p0=$(free_port)
    env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$HH" CORAL_DATA_DIR="$DD" CORAL_CLOUD_URL=off CORAL_TELEMETRY_DISABLED=1 \
      "$app/coral" --home "$DD" --no-browser --cloud-url off --host 127.0.0.1 --port "$p0" --backend pty > "$work/$n.seed.log" 2>&1 & sp=$!
    for i in $(seq 1 80); do [ -r "$DD/api_key" ] && [ -s "$DD/sessions.db" ] && sleep 2 && break; sleep 0.5; done
    kill "$sp" 2>/dev/null; wait "$sp" 2>/dev/null; rm -f "$DD/api_key"
    sqlite3 "$DD/sessions.db" < "$seed" || return 1
  fi
  PORT=$(free_port)
  env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin HOME="$HH" CORAL_DATA_DIR="$DD" CORAL_CLOUD_URL=off \
    HTTPS_PROXY="http://127.0.0.1:$pp" HTTP_PROXY="http://127.0.0.1:$pp" NO_PROXY= \
    "$app/coral-tray" --foreground --no-browser --port "$PORT" --home "$HH" --backend pty > "$work/$n.tray.out" 2>&1 & TRAY_PID=$!
  PIDS+=("$TRAY_PID"); disown "$TRAY_PID"
  for i in $(seq 1 120); do lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1 && break; sleep 0.5; done
  SRV_PID=$(lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1)
  [ -n "$SRV_PID" ] || return 1
  PIDS+=("$SRV_PID")
  for i in $(seq 1 80); do [ -r "$DD/api_key" ] && break; sleep 0.5; done
  KEY=$(tr -d '[:space:]' < "$DD/api_key" 2>/dev/null)
  [ -n "$KEY" ]
}
stop_tray() {
  for p in "$TRAY_PID" "$SRV_PID" "$PROXY_PID"; do [ -n "$p" ] && kill "$p" 2>/dev/null; done
  sleep 2
  [ -n "$SRV_PID" ] && kill -0 "$SRV_PID" 2>/dev/null && kill -9 "$SRV_PID" 2>/dev/null
  TRAY_PID="" SRV_PID="" PROXY_PID=""
}
api() { curl -s -m 30 -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' "$@"; }

if [ -n "$app" ]; then
  # ---- 3a analytics control: default settings, the proxy must SEE the analytics host ---------------------------
  control_hosts=""
  if start_tray control none; then
    for i in $(seq 1 60); do grep -q posthog "$PLOG" && break; sleep 0.5; done
    control_hosts=$(grep -c posthog "$PLOG")
  fi
  stop_tray
  # ---- the running tray for items 2-6: an operator-shaped set written by older versions, analytics off ---------
  mkdir -p "$work/wd"
  cat > "$work/seed.sql" <<SQL
INSERT OR REPLACE INTO user_settings(key,value) VALUES
('telemetry_enabled','false'),('debug_input_log','True'),('default_permission_mode','plan'),('default_agent_type','claude'),
('auto_wake_after_restart','False'),('board_auto_attach','True'),('file_search_limit','1000'),('agent_launch_limit','5'),
('hub_acts_as_operator','true'),('ui_zoom','1.2'),('theme','dark'),('cli_path_claude','$here/smoke_macos/standin_cli.py');
SQL
  if ! start_tray main "$work/seed.sql"; then
    for i in "2 loopback" "3 analytics" "4 comments" "5 multiselect" "6 settings"; do result "$i" FAIL "the tray did not start the server (see $work/main.tray.out)"; done
    app=""
  fi
fi

if [ -n "$app" ]; then
  # ---- 2 loopback ----------------------------------------------------------------------------------------------
  health=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "http://127.0.0.1:$PORT/api/health")
  listen=$(lsof -nP -a -p "$SRV_PID" -p "$TRAY_PID" -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1{print $9}' | sort -u | tr '\n' ' ' | sed 's/ $//')
  nonloop=$(printf '%s' "$listen" | tr ' ' '\n' | grep -v '^$' | grep -v '^127\.0\.0\.1:' | grep -v '^\[::1\]:' | tr '\n' ' ')
  priv=$(api "http://127.0.0.1:$PORT/api/system/privacy")
  host=$(printf '%s' "$priv" | jget effective_host)
  if [ "$health" = 200 ] && [ -n "$listen" ] && [ -z "$nonloop" ] && [ "$host" = 127.0.0.1 ]; then
    result "2 loopback" PASS "tray started the server; it listens on $listen only; privacy effective_host $host"
  else
    result "2 loopback" FAIL "health=$health listening=[$listen] not loopback=[$nonloop] effective_host=[$host]"
  fi

  # ---- 3 analytics ---------------------------------------------------------------------------------------------
  sleep 20   # the tray and the server have had their time to report an install, an upgrade, a start
  ph=$(grep -c posthog "$PLOG"); others=$(grep -v posthog "$PLOG" | sed 's/^[A-Z]* //' | sort -u | tr '\n' ' ')
  if [ -z "$control_hosts" ] || [ "$control_hosts" = 0 ]; then
    result "3 analytics" FAIL "the control (default settings) never reached the analytics host through the proxy, so a quiet run would prove nothing"
  elif [ "$ph" != 0 ]; then
    result "3 analytics" FAIL "telemetry_enabled=false and the proxy still saw $ph attempts at the analytics host (control: $control_hosts)"
  else
    result "3 analytics" PASS "control: $control_hosts attempts at the analytics host; telemetry_enabled=false: 0. Other hosts reached: ${others:-none}"
  fi

  # ---- 4 comments ----------------------------------------------------------------------------------------------
  doc=$'# Smoke plan\n\n## Steps\n\n1. Install the build.\n2. Comment on this line.\n'
  hex=$(printf '%s' "$doc" | shasum -a 256 | awk '{print $1}')
  mkdir -p "$DD/artifacts/objects"; printf '%s' "$doc" > "$DD/artifacts/objects/$hex"
  printf '{"name":"plan.md","media_type":"text/markdown","size":%s,"digest":"%s"}' "$(printf '%s' "$doc" | wc -c | tr -d ' ')" "$hex" > "$DD/artifacts/objects/$hex.json"
  c0=$(api "http://127.0.0.1:$PORT/api/artifacts/$hex/comments?state=all" | jget comments)
  mk=$(api -X POST -d '{"body":"Smoke comment.","anchor":{"quote":"Comment on this line","prefix":"2. ","suffix":".","start_line":6,"end_line":6}}' "http://127.0.0.1:$PORT/api/artifacts/$hex/comments")
  made=$(printf '%s' "$mk" | jget id)
  again=$(api "http://127.0.0.1:$PORT/api/artifacts/$hex/comments?state=all")
  n=$(printf '%s' "$again" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["comments"]))' 2>/dev/null)
  q=$(printf '%s' "$again" | jget comments.0.anchor.quote); k=$(printf '%s' "$again" | jget comments.0.author.kind)
  if [ "$c0" = "[]" ] && [ "$n" = 1 ] && [ "$q" = "Comment on this line" ] && [ "$k" = owner ]; then
    result "4 comments" PASS "empty list, comment #$made saved with its quote and the owner as author, read back"
  else
    result "4 comments" FAIL "first read [$c0], create answered [$(printf '%s' "$mk" | cut -c1-120)], then $n comment(s), quote [$q], author kind [$k]"
  fi

  # ---- 5 multiselect and 6 settings (one launch: no agent type, so the saved default and permission mode apply) ---
  launch=$(api -X POST -d "{\"working_dir\":\"$work/wd\",\"display_name\":\"smoke\"}" "http://127.0.0.1:$PORT/api/sessions/launch")
  sid=$(printf '%s' "$launch" | jget session_id); atype=$(printf '%s' "$launch" | jget agent_type)
  name="claude-$sid"; kind=""
  for i in $(seq 1 60); do
    kind=$(api "http://127.0.0.1:$PORT/api/sessions/live" | python3 -c 'import sys,json
d=json.load(sys.stdin); l=d if isinstance(d,list) else d.get("sessions",[])
print(next((s.get("waiting_kind") or "" for s in l if s.get("session_id")==sys.argv[1]), ""))' "$sid" 2>/dev/null)
    [ "$kind" = question ] && break; sleep 0.5
  done
  ans() { api -X POST -d "$1" -w '\n%{http_code}' "http://127.0.0.1:$PORT/api/sessions/live/$name/answer-prompt"; }   # body, then the status on its own line
  body() { printf '%s' "$1" | sed '$d'; }
  code() { printf '%s' "$1" | tail -1; }
  base="\"session_id\":\"$sid\",\"agent_type\":\"claude\""
  t1=$(ans "{$base,\"n\":2,\"label\":\"Doesn't show the game\"}"); t2=$(ans "{$base,\"n\":3,\"label\":\"Tone/story is off\"}")
  t3=$(ans "{$base,\"n\":3,\"label\":\"Tone/story is off\"}"); t4=$(ans "{$base,\"n\":4,\"label\":\"Looks cheap/amateur\"}")
  stale=$(ans "{$base,\"n\":1,\"label\":\"Not the label\"}")
  sub=$(ans "{$base,\"submit\":true}")
  for i in $(seq 1 20); do [ -s "$work/wd/standin-answer.txt" ] && break; sleep 0.5; done
  got=$(cat "$work/wd/standin-answer.txt" 2>/dev/null)
  flag() { body "$1" | jget checked; }
  if [ "$kind" = question ] && [ "$(flag "$t1")" = True ] && [ "$(flag "$t2")" = True ] && [ "$(flag "$t3")" = False ] && [ "$(flag "$t4")" = True ] \
     && [ "$(code "$stale")" = 409 ] && [ "$(code "$sub")" = 200 ] && body "$sub" | grep -q '"ok":true' \
     && [ "$got" = '["Doesn'"'"'t show the game", "Looks cheap/amateur"]' ]; then
    result "5 multiselect" PASS "the question was waiting_kind=question; toggles read back from the terminal, a stale label got 409, Submit ok, the CLI received exactly boxes 2 and 4"
  else
    result "5 multiselect" FAIL "waiting_kind=[$kind] toggles=[$(flag "$t1") $(flag "$t2") $(flag "$t3") $(flag "$t4")] stale=[$(code "$stale")] submit=[$(code "$sub") $(body "$sub" | cut -c1-80)] CLI got [$got] hook errors: $(cat "$work/wd/standin-hook-error.txt" 2>/dev/null | head -1)"
  fi

  priv=$(api "http://127.0.0.1:$PORT/api/system/privacy")
  tel=$(printf '%s' "$priv" | jget telemetry_enabled); rem=$(printf '%s' "$priv" | jget remote_access_enabled); dbg=$(printf '%s' "$priv" | jget debug_input_log.enabled)
  argv=$(ps -axo command= | grep -F -- "--session-id $sid" | grep -v grep | head -1)
  stored=$(sqlite3 "$DD/sessions.db" "select key||'='||value from user_settings where key in ('telemetry_enabled','debug_input_log','auto_wake_after_restart','board_auto_attach','file_search_limit','agent_launch_limit','ui_zoom','theme') order by key" | tr '\n' ' ')
  legacy=$(sqlite3 "$DD/sessions.db" "select count(*) from user_settings where key='hub_acts_as_operator'")
  want="agent_launch_limit=5 auto_wake_after_restart=False board_auto_attach=True debug_input_log=True file_search_limit=1000 telemetry_enabled=false theme=dark ui_zoom=1.2 "
  if [ "$tel" = False ] && [ "$rem" = False ] && [ "$dbg" = True ] && [ "$atype" = claude ] && printf '%s' "$argv" | grep -q -- '--permission-mode plan' \
     && [ "$stored" = "$want" ] && [ "$legacy" = 0 ]; then
    result "6 settings" PASS "privacy: telemetry off, remote off, debug input log on; a launch with no type is claude with --permission-mode plan; values kept as written; the legacy hub_acts_as_operator removed"
  else
    result "6 settings" FAIL "telemetry=[$tel] remote=[$rem] debug_input_log=[$dbg] agent_type=[$atype] permission flag in argv=[$(printf '%s' "$argv" | grep -o -- '--permission-mode [a-zA-Z]*')] stored=[$stored] legacy_rows=[$legacy]"
  fi
  stop_tray
fi

# ---- the table ------------------------------------------------------------------------------------------------
echo
echo "smoke_macos: ${tag:-$(basename "$dmg")}"
fails=0
for i in "${!NAMES[@]}"; do
  printf '  %-14s %-5s %s\n' "${NAMES[$i]}" "${STATES[$i]}" "${DETAILS[$i]}"
  [ "${STATES[$i]}" = PASS ] || fails=$((fails + 1))
done
if [ "$fails" -ne 0 ]; then echo "smoke_macos: FAIL ($fails item(s))"; exit 1; fi
echo "smoke_macos: ok (all ${#NAMES[@]} items)"
