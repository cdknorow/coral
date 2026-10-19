#!/usr/bin/env bash
# tools/epoch-gate.sh ID: the batch gate on a shared Linux test host ("epoch"), run DETACHED there (#4383).
#
#   EPOCH_HOST=epoch EPOCH_ROOT=/path/on/host tools/epoch-gate.sh v1478     (from the top of a clean worktree)
#
# It ships this worktree's tree (git archive of HEAD, so only committed files) to $EPOCH_ROOT/coralgo-check-ID on
# $EPOCH_HOST, starts the gate there under setsid/nohup (so a dropped ssh session no longer kills it: epoch lost
# the network mid-gate three times on 2026-10-17), and polls for its done-marker every minute (an ssh failure is
# retried, never fatal). Then it prints the gate's whole log and the last samples, keeps the sampler log as
# $EPOCH_ROOT/coralgo-tmp/sampler-ID.log, and removes the gate folder by its exact path.
#
# The gate (gofmt of the whole module, go vet, the signed-Operator command-line smoke, go test with the default
# and the 'dev fts5' tags and npm run test:cloud at the same time, the opt-in real-Chrome hubproxy test, then the
# full CDP set on 6 shards) runs at nice 10 and ionice idle. A 10 s sampler writes load, memory, io and memory
# pressure and the counters of $EPOCH_NIC (default enp2s0f0) next to it, so a stall leaves evidence.
#
# The host needs, under $EPOCH_ROOT: coralgo-tools/node/bin (node), coralgo-tools/chrome-linux64/chrome,
# coralgo-cache/ (Go, npm and node_modules caches) and coralgo-tmp/; Go at $GO_BIN (default ~/go-install/go/bin).
# GitHub's cdp-gate.yml runs the same CDP set without this host; this script is the fallback.
set -uo pipefail
ID=${1:?usage: tools/epoch-gate.sh ID}
case "$ID" in *[!A-Za-z0-9._-]*) echo "ID may hold only letters, digits, . _ -" >&2; exit 2 ;; esac
H=${EPOCH_HOST:-epoch}; ROOT=${EPOCH_ROOT:?set EPOCH_ROOT to the gate root on the host}; NIC=${EPOCH_NIC:-enp2s0f0}
D=coralgo-check-$ID
top=$(git rev-parse --show-toplevel) || exit 2
cd "$top" || exit 2
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "commit or stash first: the gate tests HEAD" >&2; exit 2; }
echo "gate $ID: $(git rev-parse --short HEAD) tree $(git rev-parse --short 'HEAD^{tree}') -> $H:$ROOT/$D"
git archive --format=tar HEAD coral-go tests tools installers licenses THIRD_PARTY_NOTICES_MPL.md LICENSE | gzip |
  ssh -o BatchMode=yes "$H" "umask 022; rm -rf -- '$ROOT/$D' && mkdir -p '$ROOT/$D' && cd '$ROOT/$D' && tar xzf - 2>/dev/null" || exit 1
ssh -o BatchMode=yes "$H" "cat > '$ROOT/$D/gate.sh'" <<'GATE'
umask 022
N=${EPOCH_ROOT:?}
export PATH=$N/coralgo-tools/node/bin:${GO_BIN:-$HOME/go-install/go/bin}:$PATH GOCACHE=$N/coralgo-cache/gocache GOMODCACHE=$N/coralgo-cache/gomodcache npm_config_cache=$N/coralgo-cache/npm TMPDIR=$N/coralgo-tmp GOMAXPROCS=8 GOFLAGS=-p=8
unset CORAL_CLOUD_URL CORAL_DIR CORAL_DATA_DIR CORAL_URL TMUX
export CORAL_TELEMETRY_DISABLED=1 # our own runs send no usage events (#4936)
cd $N/$D/coral-go
echo "gofmt-all:[$("$(go env GOROOT)/bin/gofmt" -l .)] (gofmt of $(go version))"
ts(){ echo "[$(date -u +%H:%M:%S)] $*"; }
ts start
nice -n 10 ionice -c3 go vet ./... && echo vet-ok
ts vet done
# The person at a terminal: coral-board join, post and read as the signed Operator against a fresh prod server
# (tests/release/operator_cli_smoke.sh; v1.4.72's refusal of the Operator's own command line was caught only by
# the release smoke). A random high port, as epoch is shared.
if [ -f ../tests/release/operator_cli_smoke.sh ]; then
  OB=$(mktemp -d "$N/coralgo-tmp/opbin.XXXXXX")
  if nice -n 10 ionice -c3 go build -tags fts5 -o "$OB/" ./cmd/coral ./cmd/coral-board > ../opsmoke.log 2>&1 && \
     TMPDIR=$N/coralgo-tmp nice -n 10 ionice -c3 bash ../tests/release/operator_cli_smoke.sh "$OB" $((20000 + RANDOM % 20000)) >> ../opsmoke.log 2>&1; then
    tail -1 ../opsmoke.log
  else
    echo "operator_cli_smoke FAILED:"; tail -8 ../opsmoke.log
  fi
  rm -rf -- "$OB"
  ts operator cli smoke done
fi
# One shared node_modules for the team's checks, refreshed when package.json changes.
NM=$N/coralgo-cache/frontend
mkdir -p $NM && if ! cmp -s ../tests/frontend/package.json $NM/package.json; then cp ../tests/frontend/package.json $NM/ && (cd $NM && npm install --no-audit --no-fund --no-package-lock >/dev/null 2>&1); fi
ln -sfn $NM/node_modules ../tests/frontend/node_modules
# The two go test tag sets and test:cloud at the same time (36 cores; GOMAXPROCS=8 and -p=8 each).
# Each writes its own whole log and its own exit status; nothing is read until all three end.
rm -f ../gotest.rc ../gotestdev.rc ../cloud.rc
( nice -n 10 ionice -c3 go test -count=1 ./... > ../gotest.log 2>&1; echo $? > ../gotest.rc ) &
( nice -n 10 ionice -c3 go test -count=1 -tags 'dev fts5' ./... > ../gotestdev.log 2>&1; echo $? > ../gotestdev.rc ) &
( cd ../tests/frontend && nice -n 10 ionice -c3 npm run -s test:cloud > ../../cloud.log 2>&1; echo $? > ../../cloud.rc ) &
wait
ts go test, go test dev fts5 and test:cloud done
rc_of(){ [ -s "$1" ] && cat "$1" || echo missing; }
r=$(rc_of ../gotest.rc); if [ "$r" = 0 ]; then echo "go test ok"; else echo "go test FAILED (rc=$r):"; grep -E "^(FAIL|--- FAIL|panic:)" ../gotest.log; echo "  failing output (each --- FAIL and the 30 lines after it):"; grep -A30 -E "^(\s*--- FAIL|panic:)" ../gotest.log | head -200 | sed "s/^/    /"; fi
r=$(rc_of ../gotestdev.rc); if [ "$r" = 0 ]; then echo "go test (dev fts5) ok"; else echo "go test (dev fts5) FAILED (rc=$r):"; grep -E "^(FAIL|--- FAIL|panic:)" ../gotestdev.log; echo "  failing output (each --- FAIL and the 30 lines after it):"; grep -A30 -E "^(\s*--- FAIL|panic:)" ../gotestdev.log | head -200 | sed "s/^/    /"; fi
r=$(rc_of ../cloud.rc); if [ "$r" = 0 ]; then echo "test:cloud ok"; else echo "test:cloud FAILED (rc=$r)"; tail -5 ../cloud.log; fi
# The opt-in real-Chrome hubproxy test (#3663): ok only when both tests ran and passed, never on a skip.
if [ -f pkg/hubproxy/ws_busy_chrome_test.go ]; then CORAL_CHROME_TEST=1 CORAL_CHROME=$N/coralgo-tools/chrome-linux64/chrome nice -n 10 ionice -c3 go test -count=1 -run Chrome4429 -v ./pkg/hubproxy > ../chrome4429.log 2>&1; np=$(grep -c -E '^--- PASS: TestChrome4429' ../chrome4429.log); if [ "$np" = 2 ]; then echo "chrome4429 ok (2 PASS)"; else echo "chrome4429 FAILED (PASS=$np):"; grep -E '^(--- |FAIL|ok)|_test.go:' ../chrome4429.log | head -15; fi; fi
ts chrome4429 done
cd ../tests/frontend
nice -n 10 ionice -c3 node run_cdp_suites.mjs --shards 6 --timeout 180 --chrome $N/coralgo-tools/chrome-linux64/chrome 2>&1 | grep -E "^FAIL|^FLAKY|^TIMEOUT|suites passed|^results:"
ts cdp done
GATE
ssh -o BatchMode=yes "$H" "cd '$ROOT/$D' && cat > sampler.sh <<'SMP'
while [ ! -f gate.done ]; do
  { date -u +%T; cat /proc/loadavg; free -m | sed -n 2p; cat /proc/pressure/io /proc/pressure/memory | tr '\n' ' '; echo; grep '$NIC' /proc/net/dev; } >> sampler.log
  sleep 10
done
SMP
EPOCH_ROOT='$ROOT' D='$D' setsid nohup bash -c 'bash sampler.sh & bash gate.sh > gate.log 2>&1; echo \"gate-rc=\$?\" >> gate.log; touch gate.done' > /dev/null 2>&1 < /dev/null & echo started" || exit 1
while :; do
  r=$(ssh -o ConnectTimeout=15 -o BatchMode=yes "$H" "test -f '$ROOT/$D/gate.done' && echo done" 2>/dev/null)
  [ "$r" = "done" ] && break
  sleep 60
done
ssh -o BatchMode=yes "$H" "cat '$ROOT/$D/gate.log'; echo '--- sampler (last 6)'; tail -6 '$ROOT/$D/sampler.log'; cp '$ROOT/$D/sampler.log' '$ROOT/coralgo-tmp/sampler-$D.log'; rm -rf -- '$ROOT/$D'"
