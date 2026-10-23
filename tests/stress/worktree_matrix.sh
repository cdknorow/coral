#!/usr/bin/env bash
#
# The git worktree matrix (#6693, from #6692): every way Coral makes a git worktree, through the real API of an isolated server, against repositories in the states users
# have. It is a FINDINGS run, not a pass/fail test: each case prints what the API answered, the exact git error it carries, and what was left behind (a folder, a branch, a
# registration). Exit status is 0 whenever the run itself worked. Runs on Linux, macOS and Windows (Git Bash, CORAL_TEST_BACKEND=pty).
#
# What makes a worktree: a team launch with "worktree": true (routes/sessions_launch_team.go createTeamWorktree), a stopped team's resurrect (sessions_lifecycle.go
# ResurrectTeam), and a task or job run (background/scheduler.go prepareRunWorktree, here through POST /api/tasks/run).
#
# Safe by construction: its own HOME, data folder, repositories and stub agent CLIs under one temp folder, port refused if it is 8420, the agents are mock-agent. It
# removes only what it made, by exact path, and kills only the processes it started (recorded pids).
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/agent_env.sh"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/../lib/backend.sh"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CORAL_DIR="$REPO_ROOT/coral-go"
PORT=${CORAL_TEST_PORT:-8476}
if [[ "$PORT" == 8420 ]]; then echo "ERROR: refusing port 8420 (production Coral server); pick another port." >&2; exit 2; fi
HOST="127.0.0.1"
BASE_URL="http://${HOST}:${PORT}"
SERVER_PID=""
OUT_DIR="${WORKTREE_MATRIX_OUT:-}"   # where the table is also written (a CI step uploads it); optional

log() { echo "[worktree-matrix] $*"; }

TMPROOT="$(mktemp -d "${TMPDIR:-/tmp}/coral-test-wtm.XXXXXX")"
export TMPDIR="$TMPROOT"
cleanup() {
    local code=$?
    stop_server
    coral_test_sandbox_cleanup 2>/dev/null || true
    if [[ "${KEEP_TEST_DATA:-0}" == 1 || $code -ne 0 ]]; then log "test data kept: $TMPROOT"; else coral_test_rm_tree "$TMPROOT"; fi
}
trap cleanup EXIT

api() { local method="$1" path="$2"; shift 2; curl -s -m 60 -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "$@"; }
# api_code METHOD PATH [curl args]: the body, then the status on the last line
api_code() { local method="$1" path="$2"; shift 2; curl -s -m 90 -w '\n%{http_code}' -X "$method" "${BASE_URL}${path}" -H "Content-Type: application/json" "$@"; }
jget() { python3 -c "import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print(''); sys.exit(0)
v=d
for k in sys.argv[1].split('.'):
    v=v.get(k,'') if isinstance(v,dict) else ''
print('' if v is None else (v if not isinstance(v,(dict,list)) else json.dumps(v)))" "$1" 2>/dev/null | tr -d '\r'; }

wait_for_server() {
    local n=40
    while ! curl -s -m 5 "${BASE_URL}/api/health" >/dev/null 2>&1; do n=$((n - 1)); if [[ $n -le 0 ]]; then log "the server did not start"; return 1; fi; sleep 0.5; done
}

stop_server() {
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; fi
    SERVER_PID=""
}

# start_server DATA_DIR: the isolated server on that data folder
start_server() {
    stop_server
    export CORAL_DATA_DIR="$1"
    mkdir -p "$CORAL_DATA_DIR"
    env -u CORAL_PORT -u CORAL_URL -u CORAL_SESSION_NAME -u CORAL_SUBSCRIBER_ID -u CLAUDE_PROJECTS_DIR -u GEMINI_TMP_DIR -u ANTIGRAVITY_DATA_DIR -u PI_CODING_AGENT_DIR \
        HOME="$TMPROOT/host-home" USERPROFILE="$TMPROOT/host-home" PATH="$CORAL_TEST_STUB_BIN:$PATH" \
        "$TMPROOT/coral${CORAL_TEST_EXE}" --home "$(coral_test_native_path "$CORAL_DATA_DIR")" --no-browser --cloud-url off --host "$HOST" --port "$PORT" --backend "$CORAL_TEST_BACKEND" >"$TMPROOT/server.log" 2>&1 &
    SERVER_PID=$!
    wait_for_server || return 1
    coral_test_require_ok "$(api PUT "/api/settings" -d "{\"cli_path_claude\": \"$MOCK_AGENT_NATIVE\"}")" "cli_path_claude=$MOCK_AGENT_NATIVE" >/dev/null
}

export CORAL_NO_DEMO_SEED=1 CORAL_NO_WELCOME=1 CORAL_TMUX_NO_FALLBACK=1
mkdir -p "$TMPROOT/host-home" "$TMPROOT/repos"
log "git: $(git --version)   os: $(uname -s)   backend: $CORAL_TEST_BACKEND"
log "building coral (dev) and mock-agent ..."
(cd "$CORAL_DIR" && go build -tags "dev fts5" -o "$TMPROOT/" ./cmd/coral ./cmd/coral-board ./cmd/coral-agent ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync 2>&1 | tail -5; go build -o "$TMPROOT/mock-agent${CORAL_TEST_EXE}" ./cmd/mock-agent/) || { echo "build failed" >&2; exit 2; }
MOCK_AGENT_NATIVE="$(coral_test_native_path "$TMPROOT/mock-agent${CORAL_TEST_EXE}")"
coral_test_sandbox
start_server "$TMPROOT/coral-data" || { echo "the first server did not start" >&2; tail -20 "$TMPROOT/server.log" >&2; exit 2; }

GIT_ID=(-c user.name=Test -c user.email=test@test.invalid)
# newrepo NAME [BRANCH]: a repository with one commit, on BRANCH (main by default); prints its path
newrepo() {
    local d="$TMPROOT/repos/$1"
    mkdir -p "$d"
    git -C "$d" init -q -b "${2:-main}" 2>/dev/null || { git -C "$d" init -q; git -C "$d" checkout -q -b "${2:-main}"; }
    echo "hello" >"$d/README.md"
    git -C "$d" add . && git "${GIT_ID[@]}" -C "$d" commit -q -m initial
    printf '%s' "$d"
}
native() { coral_test_native_path "$1"; }

ROWS="$TMPROOT/rows.tsv"; : >"$ROWS"
N=0
# one-line, tab-free, path-free text for the table
clean() { tr '\r\n\t' '   ' | sed -e "s#$TMPROOT#<tmp>#g" -e 's#[A-Za-z]:/[^ ]*coral-test-wtm\.[A-Za-z0-9]*#<tmp>#g' | cut -c1-"${2:-420}"; }

# state REPO: what is left in the repository and the data folder
state() {
    local repo="$1"
    local wts brs dirs
    wts=$(git -C "$repo" worktree list --porcelain 2>/dev/null | grep -c '^worktree ' | tr -d ' ')
    brs=$(git -C "$repo" branch --list 'coral-team/*' 'coral/*' 2>/dev/null | tr -d ' *\r' | tr '\n' ',')
    dirs=$(ls "$CORAL_DATA_DIR/worktrees" 2>/dev/null | tr '\n' ',')
    printf 'worktrees(incl main)=%s branches=[%s] folders=[%s]' "$wts" "$brs" "$dirs" | sed -e 's#b-[0-9a-f]\{6,\}#b-<hash>#g'
}

# row CASE WHAT HTTP RESULT LEFT
row() {
    N=$((N + 1)); printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$5" >>"$ROWS"; log "[$1] http=$3 | $4 | $5" | cut -c1-1200
    if [[ -n "$OUT_DIR" ]]; then mkdir -p "$OUT_DIR"; cp "$ROWS" "$OUT_DIR/rows.tsv" 2>/dev/null || true; fi
}

# launch_team CASE WHAT REPO [BASE_BRANCH] [BOARD]: one team launch with a worktree; records the row; leaves the launch result in LAST_BODY
LAST_BODY=""; LAST_CODE=""
launch_team() {
    local case="$1" what="$2" repo="$3" base="${4:-}" board="${5:-wm-$1-$$}"
    local basej=""; [[ -n "$base" ]] && basej="\"base_branch\": \"$base\","
    local resp bodyfile="$TMPROOT/body.json"
    # the body goes through a file, as UTF-8 bytes: a curl.exe command-line argument is converted through the ANSI code page on Windows, which turns accents and CJK into '?'
    printf '{"board_name": "%s", "working_dir": "%s", "worktree": true, %s "agents": [{"agent_type": "claude", "name": "Dev", "prompt": "work"}]}' "$board" "$(native "$repo")" "$basej" >"$bodyfile"
    resp=$(api_code POST "/api/sessions/launch-team" --data-binary "@$(native "$bodyfile")")
    LAST_CODE="${resp##*$'\n'}"; LAST_BODY="${resp%$'\n'*}"
    local ok err wt
    ok=$(printf '%s' "$LAST_BODY" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); print('ok' if (d.get('ok') or d.get('sessions') or d.get('agents')) and not d.get('error') else 'FAILED')
except Exception: print('FAILED')" 2>/dev/null | tr -d '\r')
    err=$(printf '%s' "$LAST_BODY" | jget error)
    [[ -z "$err" ]] && err=$(printf '%s' "$LAST_BODY" | jget message)
    wt=$(printf '%s' "$LAST_BODY" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); print(next((a.get('worktree_path','') for a in d.get('agents',[]) if a.get('worktree_path')),''))
except Exception: print('')" 2>/dev/null | tr -d '\r')
    row "$case" "$what" "$LAST_CODE" "$ok${err:+ ($(printf '%s' "$err" | clean x 1000))}${wt:+ [worktree made]}" "$(state "$repo")"
}

# kill_launch: kills the sessions of LAST_BODY (the worktree is removed by the kill)
kill_launch() {
    local info sname sid
    info=$(printf '%s' "$LAST_BODY" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin)
    for a in d.get('agents',[]): print(a.get('session_name','')+' '+a.get('session_id',''))
except Exception: pass" 2>/dev/null | tr -d '\r')
    while IFS=' ' read -r sname sid; do
        [[ -n "$sname" && -n "$sid" ]] && curl -s -m 40 -X POST "${BASE_URL}/api/sessions/live/${sname}/kill" -H "Content-Type: application/json" -d "{\"session_id\": \"$sid\"}" >/dev/null 2>&1
    done <<<"$info"
    sleep 2
}

# kill_team: stops the team of LAST_BODY the way the dashboard does (POST /api/sessions/live/team/<board>/kill), then waits for the asynchronous worktree cleanup
kill_team() {
    local bid code
    bid=$(printf '%s' "$LAST_BODY" | jget board_id)
    [[ -z "$bid" ]] && { log "kill_team: no board_id"; return 1; }
    code=$(curl -s -m 60 -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/api/sessions/live/team/${bid}/kill" -H "Content-Type: application/json" -d '{}')
    sleep 6
    printf '%s' "$code"
}

# ── 1. controls ──────────────────────────────────────────────────────
R=$(newrepo control)
launch_team control-main "a repository on main, base main (the case that must work)" "$R"; kill_launch
row control-after-session-kill "after the agent's session was killed one by one (POST /api/sessions/live/<name>/kill)" "-" "kill done" "$(state "$R")"
R=$(newrepo control-team-kill)
launch_team control-team "a second control, to be stopped as a team" "$R" main "wm-ctk-$$"
code=$(kill_team)
row control-after-team-kill "after the team was stopped (POST /api/sessions/live/team/<board>/kill: the dashboard's Stop team)" "$code" "team stop answered" "$(state "$R")"

# ── 2. the base branch ───────────────────────────────────────────────
R=$(newrepo default-master master)
launch_team base-default-on-master "repository on master, no base_branch (Coral assumes main)" "$R"; kill_launch
launch_team base-master-named "same repository, base_branch=master named" "$R" master "wm-bm-$$"; kill_launch

R="$TMPROOT/repos/no-commits"; mkdir -p "$R"; git -C "$R" init -q -b main 2>/dev/null || git -C "$R" init -q
launch_team no-commits "fresh repository, no commit at all" "$R"; kill_launch

R=$(newrepo detached); git -C "$R" checkout -q --detach
launch_team detached-head "HEAD detached, base main" "$R"; kill_launch

R=$(newrepo base-missing)
launch_team base-missing "base_branch names a branch that does not exist" "$R" "no-such-branch"; kill_launch

# a branch that exists only on the remote
ORIGIN="$TMPROOT/repos/origin.git"; SEED=$(newrepo seed)
git -C "$SEED" checkout -q -b feature; echo f >"$SEED/f.txt"; git -C "$SEED" add .; git "${GIT_ID[@]}" -C "$SEED" commit -q -m feature; git -C "$SEED" checkout -q main
git clone -q --bare "$SEED" "$ORIGIN" 2>/dev/null
CLONE="$TMPROOT/repos/remote-only"; git clone -q "$(native "$ORIGIN")" "$CLONE" 2>/dev/null
launch_team base-remote-only "base_branch=feature, a branch only on origin (no local branch)" "$CLONE" feature "wm-rem-$$"; kill_launch
launch_team base-origin-prefixed "base_branch=origin/feature" "$CLONE" "origin/feature" "wm-rem2-$$"; kill_launch

# ── 3. the repository's state ────────────────────────────────────────
R=$(newrepo dirty); echo changed >>"$R/README.md"; echo new >"$R/untracked.txt"
launch_team dirty-tree "modified tracked file and an untracked file" "$R"; kill_launch

R=$(newrepo index-lock); : >"$R/.git/index.lock"
launch_team index-lock "a leftover .git/index.lock in the repository" "$R"; kill_launch; rm -f "$R/.git/index.lock"

R=$(newrepo head-lock); : >"$R/.git/HEAD.lock"
launch_team head-lock "a leftover .git/HEAD.lock" "$R" main "wm-hl-$$"; kill_launch; rm -f "$R/.git/HEAD.lock"

R=$(newrepo crlf); git -C "$R" config core.autocrlf true; git -C "$R" config core.safecrlf true
printf '* text=auto eol=crlf\n' >"$R/.gitattributes"; printf 'a\nb\r\nc\n' >"$R/mixed.txt"; git -C "$R" add -A 2>/dev/null; git "${GIT_ID[@]}" -C "$R" commit -q -m crlf 2>/dev/null
launch_team crlf-config "core.autocrlf=true, core.safecrlf=true, eol=crlf attributes, a mixed-ending file" "$R"; kill_launch

R=$(newrepo has-worktree-already)
git -C "$R" worktree add -q -b other "$TMPROOT/repos/has-worktree-other" 2>/dev/null
launch_team repo-has-other-worktree "the repository already has another linked worktree" "$R"; kill_launch

R=$(newrepo as-worktree-src); git -C "$R" worktree add -q -b wt-as-repo "$TMPROOT/repos/as-worktree" 2>/dev/null
launch_team repo-is-a-worktree "working_dir is itself a linked worktree" "$TMPROOT/repos/as-worktree" "" "wm-aw-$$"; kill_launch

SUPER=$(newrepo super); SUBSRC=$(newrepo subsrc)
git -c protocol.file.allow=always "${GIT_ID[@]}" -C "$SUPER" submodule add -q "$(native "$SUBSRC")" sub 2>/dev/null; git "${GIT_ID[@]}" -C "$SUPER" commit -q -m sub 2>/dev/null
launch_team repo-is-a-submodule "working_dir is a submodule's folder" "$SUPER/sub" "" "wm-sm-$$"; kill_launch

BARE="$TMPROOT/repos/bare.git"; git init -q --bare "$BARE" 2>/dev/null
launch_team repo-is-bare "working_dir is a bare repository" "$BARE" "" "wm-bare-$$"; kill_launch

# ── 4. paths ─────────────────────────────────────────────────────────
R=$(newrepo "my repo (spaces)")
launch_team path-spaces "repository path with spaces and parentheses" "$R"; kill_launch
R=$(newrepo "dépôt-ünï-日本")
launch_team path-non-ascii "repository path with accents and CJK" "$R"; kill_launch

# repositories under long paths: one under the 260-character limit, one over it
for SEGS in 3 6; do
    LONG="$TMPROOT/repos"; for i in $(seq 1 $SEGS); do LONG="$LONG/segment-number-$i-with-a-rather-long-folder-name"; done
    mkdir -p "$LONG" 2>/dev/null; R="$LONG/repo"; mkdir -p "$R" 2>/dev/null
    MADE=yes; { git -C "$R" init -q -b main && echo x >"$R/README.md" && git -C "$R" add . && git "${GIT_ID[@]}" -C "$R" commit -q -m initial; } >/dev/null 2>&1 || MADE=no
    launch_team "path-long-repo-$SEGS" "repository under a path of $(native "$R" | wc -c | tr -d ' ') characters (git itself could create it: $MADE)" "$R" main "wm-lr$SEGS-$$"; kill_launch
done

# a repository that has a tracked file with a very long path (checked out into the worktree under <data>/worktrees/b-hash/...)
R=$(newrepo long-file); DEEP=""; for i in 1 2 3 4 5 6 7 8; do DEEP="$DEEP/directory-name-number-$i-padding-padding"; done
blob=$(echo long | git -C "$R" hash-object -w --stdin); git -C "$R" update-index --add --cacheinfo "100644,$blob,deep${DEEP}/file.txt" 2>/dev/null; git "${GIT_ID[@]}" -C "$R" commit -q -m deep 2>/dev/null
launch_team long-file-path "a tracked file whose path inside the repository is ${#DEEP} characters (core.longpaths unset)" "$R"; kill_launch

# a board name that is a Windows device name
R=$(newrepo reserved-name)
launch_team board-name-con "board_name is the Windows device name 'con'" "$R" main "con"; kill_launch
launch_team board-name-nul "board_name 'nul'" "$R" main "nul"; kill_launch

# ── 5. what is already there ─────────────────────────────────────────
R=$(newrepo twice)
launch_team same-team-twice-1 "the same board name launched twice (first)" "$R" main "wm-twice-$$"
FIRST_BODY="$LAST_BODY"
launch_team same-team-twice-2 "the same board name launched twice (second, first still running)" "$R" main "wm-twice-$$"
SECOND_BODY="$LAST_BODY"; LAST_BODY="$FIRST_BODY"; kill_launch; LAST_BODY="$SECOND_BODY"; kill_launch

R=$(newrepo stale)
launch_team stale-1 "stale registration: launch, then the worktree folder is deleted by hand (first)" "$R" main "wm-stale-$$"
WT=$(printf '%s' "$LAST_BODY" | python3 -c "import sys,json
try:
    d=json.load(sys.stdin); print(next((a.get('worktree_path','') for a in d.get('agents',[]) if a.get('worktree_path')),''))
except Exception: print('')" 2>/dev/null | tr -d '\r')
STALE_BODY="$LAST_BODY"
if [[ -n "$WT" ]]; then rm -rf "$(cygpath -u "$WT" 2>/dev/null || printf '%s' "$WT")"; fi
launch_team stale-2 "stale registration left by that deletion, then another team in the same repository" "$R" main "wm-stale2-$$"
S2="$LAST_BODY"; LAST_BODY="$STALE_BODY"; kill_launch; LAST_BODY="$S2"; kill_launch
git -C "$R" worktree prune 2>/dev/null

# four teams at once in one repository
R=$(newrepo parallel); PAR="$TMPROOT/par"; mkdir -p "$PAR"
PAR_PIDS=()
for i in 1 2 3 4; do
    ( api_code POST "/api/sessions/launch-team" -d "{\"board_name\": \"wm-par-$i-$$\", \"working_dir\": \"$(native "$R")\", \"worktree\": true, \"agents\": [{\"agent_type\": \"claude\", \"name\": \"Dev\", \"prompt\": \"work\"}]}" >"$PAR/$i.out" 2>&1 ) &
    PAR_PIDS+=($!)
done
for p in "${PAR_PIDS[@]}"; do wait "$p"; done   # never a bare wait: the server is this shell's own background job
PAR_SUMMARY=""
for i in 1 2 3 4; do
    code=$(tail -1 "$PAR/$i.out" | tr -d '\r'); body=$(sed '$d' "$PAR/$i.out")
    e=$(printf '%s' "$body" | jget error); PAR_SUMMARY="$PAR_SUMMARY #$i:$code${e:+ ($(printf '%s' "$e" | clean x 160))}"
    LAST_BODY="$body"; kill_launch
done
row parallel-4-teams "four teams launched at the same moment in one repository" "-" "$PAR_SUMMARY" "$(state "$R")"

# ── 6. the data folder ───────────────────────────────────────────────
SPACED="$TMPROOT/data dir with spaces/and (parens)"; mkdir -p "$SPACED"
start_server "$SPACED" || log "the server did not start on the spaced data folder"
R=$(newrepo data-spaces)
launch_team data-dir-spaces "the server's data folder has spaces and parentheses (worktrees go under it)" "$R"; kill_launch

for SEGS in 3 6; do
    LONGD="$TMPROOT/data$SEGS"; for i in $(seq 1 $SEGS); do LONGD="$LONGD/a-long-data-folder-segment-$i-for-the-path"; done
    if start_server "$LONGD"; then
        R=$(newrepo "data-long-$SEGS")
        launch_team "data-dir-long-$SEGS" "the server's data folder path is $(native "$LONGD" | wc -c | tr -d ' ') characters" "$R" main "wm-dl$SEGS-$$"; kill_launch
    else
        row "data-dir-long-$SEGS" "the server's data folder path is $(native "$LONGD" | wc -c | tr -d ' ') characters" "-" "SERVER DID NOT START: $(grep -a 'Failed to start\|data folder' "$TMPROOT/server.log" | tail -3 | clean x 700)" "-"
    fi
done
start_server "$TMPROOT/coral-data2" || { echo "the final server did not start" >&2; exit 2; }

# ── 7. kill, resurrect and restart ───────────────────────────────────
R=$(newrepo resurrect)
launch_team resurrect-launch "team launched (to be stopped and resurrected)" "$R" main "wm-res-$$"
RES_BODY="$LAST_BODY"; BID=$(printf '%s' "$RES_BODY" | jget board_id)
kill_team >/dev/null
TSTAT=""
for _ in $(seq 1 20); do TSTAT=$(api GET "/api/teams/detail/${BID}" | jget status); [[ "$TSTAT" == stopped ]] && break; sleep 1; done
row resurrect-team-status "the team's status after Stop team (waited up to 20 s)" "-" "status=$TSTAT" "$(state "$R")"
if [[ -n "$BID" ]]; then
    resp=$(api_code POST "/api/teams/detail/${BID}/resurrect" -d '{}'); code="${resp##*$'\n'}"; body="${resp%$'\n'*}"
    row resurrect-after-kill "stopped team resurrected (POST /api/teams/detail/<board>/resurrect)" "$code" "$(printf '%s' "$body" | jget error | clean x 400)${body:+ | $(printf '%s' "$body" | clean x 200)}" "$(state "$R")"
    LAST_BODY="$body"; kill_launch
else
    row resurrect-after-kill "stopped team resurrected" "-" "no board_id in the launch answer: $(printf '%s' "$RES_BODY" | clean x 200)" "$(state "$R")"
fi

# a task run with create_worktree, twice (the previous run's worktree not removed), and with a bad base
R=$(newrepo task)
for k in 1 2; do
    resp=$(api_code POST "/api/tasks/run" -d "{\"prompt\": \"x\", \"repo_path\": \"$(native "$R")\", \"agent_type\": \"claude\", \"create_worktree\": true, \"max_duration_s\": 20, \"cleanup_worktree\": false}")
    code="${resp##*$'\n'}"; body="${resp%$'\n'*}"; rid=$(printf '%s' "$body" | jget run_id); [[ -z "$rid" ]] && rid=$(printf '%s' "$body" | jget id)
    sleep 8
    st=$(api GET "/api/tasks/runs/${rid}"); status=$(printf '%s' "$st" | jget status); em=$(printf '%s' "$st" | jget error_msg)
    row "task-run-$k" "task run with create_worktree=true, cleanup off (run $k of 2)" "$code" "status=$status${em:+ error=$(printf '%s' "$em" | clean x 360)}" "$(state "$R")"
done
R=$(newrepo task-master master)
resp=$(api_code POST "/api/tasks/run" -d "{\"prompt\": \"x\", \"repo_path\": \"$(native "$R")\", \"agent_type\": \"claude\", \"create_worktree\": true, \"max_duration_s\": 20}")
code="${resp##*$'\n'}"; body="${resp%$'\n'*}"; rid=$(printf '%s' "$body" | jget run_id); [[ -z "$rid" ]] && rid=$(printf '%s' "$body" | jget id)
sleep 8; st=$(api GET "/api/tasks/runs/${rid}")
row task-run-master "task run on a repository whose branch is master (no base_branch)" "$code" "status=$(printf '%s' "$st" | jget status) error=$(printf '%s' "$st" | jget error_msg | clean x 360)" "$(state "$R")"

# ── the table ────────────────────────────────────────────────────────
TABLE="$TMPROOT/worktree_matrix.md"
{
    echo "# worktree matrix: $(uname -s), $(git --version), backend $CORAL_TEST_BACKEND"
    echo
    echo "| case | what | http | answer | left behind |"
    echo "|---|---|---|---|---|"
    while IFS=$'\t' read -r a b c d e; do printf '| %s | %s | %s | %s | %s |\n' "$a" "$b" "$c" "$d" "$e"; done <"$ROWS"
} >"$TABLE"
echo; cat "$TABLE"
if [[ -n "$OUT_DIR" ]]; then mkdir -p "$OUT_DIR"; cp "$TABLE" "$OUT_DIR/worktree_matrix.md"; sed -e "s#$TMPROOT#<tmp>#g" "$TMPROOT/server.log" | grep -aE "worktree|launch-team|resurrect|kill-team" | tail -80 >"$OUT_DIR/server_worktree_lines.log" 2>/dev/null || true; fi
coral_test_sandbox_check || log "WARNING: the sandbox check failed (a real CLI may have been reached)"
exit 0
