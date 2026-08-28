#!/usr/bin/env bash
# Re-verify CLAIM_LEDGER.md row 5 (scheduled jobs) against a build.
#
# Reproduces the #43 defect exactly: a job on the DOCUMENTED DEFAULT
# (base_branch "main", create_worktree true) against a repo with main checked out.
#
# Usage: bash specs/gtm/verify-scheduled-jobs.sh /path/to/coral-binary
set -uo pipefail
BIN="${1:?usage: $0 /path/to/coral}"
PORT=8456; DIR=/tmp/coral-v43; REPO=/tmp/coral-v43-repo

[ -x "$BIN" ] || { echo "FAIL: not executable: $BIN"; exit 2; }
rm -rf "$DIR" "$REPO"; mkdir -p "$DIR" "$REPO"

# fixture: a repo with main checked out — the condition that triggers the defect
git -C "$REPO" init -q
echo x > "$REPO/f.txt"; git -C "$REPO" add -A
git -C "$REPO" -c user.email=t@t -c user.name=t commit -qm init
BRANCH=$(git -C "$REPO" branch --show-current)
echo "fixture repo on branch: $BRANCH  (must be checked out for a valid test)"
[ -n "$BRANCH" ] || { echo "FALSE PASS GUARD: no branch checked out"; exit 2; }

"$BIN" --home "$DIR" --host 127.0.0.1 --port $PORT --no-browser >"$DIR/server.log" 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; rm -rf "$REPO"' EXIT
up=0
for i in $(seq 1 60); do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/?skip_activation=1" 2>/dev/null)" = "200" ] && { up=1; break; }
  sleep 0.5
done
[ "$up" = 1 ] || { echo "FAIL: server never came up"; exit 2; }

JOB=$(curl -s -X POST "http://127.0.0.1:$PORT/api/scheduled/jobs" -H 'Content-Type: application/json' -d "{
  \"name\":\"row5-check\",\"cron_expr\":\"* * * * *\",\"timezone\":\"America/Los_Angeles\",
  \"agent_type\":\"claude\",\"repo_path\":\"$REPO\",\"base_branch\":\"$BRANCH\",
  \"prompt\":\"Reply with exactly ROW5-OK and nothing else. Do not use coral-board.\",
  \"enabled\":1,\"max_duration_s\":120,\"cleanup_worktree\":1,\"job_type\":\"agent\"}")
JID=$(echo "$JOB" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
[ -n "$JID" ] || { echo "FAIL: job not created: $JOB"; exit 2; }
echo "job $JID created with base_branch=$BRANCH (the documented default condition)"

echo "waiting for a run (cron fires each minute)..."
RUNS=0
for i in $(seq 1 30); do
  RUNS=$(curl -s "http://127.0.0.1:$PORT/api/scheduled/jobs/$JID/runs" | python3 -c "import json,sys;print(len(json.load(sys.stdin).get('runs',[])))" 2>/dev/null || echo 0)
  [ "$RUNS" -ge 1 ] && break
  sleep 10
done
[ "$RUNS" -ge 1 ] || { echo "FALSE PASS GUARD: cron never fired in ~5min — cannot judge row 5"; exit 2; }

curl -s -X POST "http://127.0.0.1:$PORT/api/scheduled/jobs/$JID/toggle" -o /dev/null
echo
curl -s "http://127.0.0.1:$PORT/api/scheduled/jobs/$JID/runs" | python3 -c "
import json,sys
runs=json.load(sys.stdin).get('runs',[])
r=runs[-1]
print('runs observed :', len(runs))
print('status        :', r['status'])
print('worktree      :', r.get('worktree_path'))
print('error         :', (r.get('error_msg') or 'none')[:160])
print()
err = r.get('error_msg') or ''
if 'already used by worktree' in err:
    print('RESULT: ROW 5 STILL ⛔ — the documented default still fails'); sys.exit(1)
if r['status'] == 'failed':
    print('RESULT: ROW 5 STILL ⛔ — run failed for a DIFFERENT reason (read error above)'); sys.exit(1)
if not r.get('worktree_path'):
    print('RESULT: INCONCLUSIVE — run did not fail but no worktree was recorded'); sys.exit(2)
print('RESULT: ROW 5 FIXED on the default — worktree created, no worktree-collision error.')
print('NOTE: check status above. \"killed\"/timeout is a SEPARATE known issue (interactive agent never exits).')
"
