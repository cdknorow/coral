#!/usr/bin/env bash
# tools/devops/prepush.sh WORKTREE: every check a ci/ batch must pass before it is pushed for the gate (#4605).
# gofmt (the module's own), build, vet (default, dev fts5, GOOS=windows), the route regenerators (must leave no diff),
# go test ./... on both tag sets in a short temp dir, and test:cloud. Exits non-zero on the first failure; logs go next to
# the worktree. Never touches ~/.coral or a running Coral; CORAL_CLOUD_URL is unset.
set -o pipefail
W=$(cd "${1:?usage: prepush.sh WORKTREE}" && pwd); cd "$W" || exit 1
. "$W/tools/devops/lib.sh"
unset CORAL_CLOUD_URL
LOGS=$(dirname "$W")/prepush-$(basename "$W"); mkdir -p "$LOGS"
T=$(mktemp -d /tmp/x.XXXX); T2=$(mktemp -d /tmp/x.XXXX)
trap 'rm -rf "$T" "$T2"; [ -L "$W/tests/frontend/node_modules" ] && rm "$W/tests/frontend/node_modules"' EXIT INT TERM
cd "$W/coral-go" || exit 1
F=$("$(go env GOROOT)/bin/gofmt" -l .); [ -z "$F" ] || die "gofmt: $F"; echo gofmt-ok
go build -o /dev/null ./... && go vet ./... && go vet -tags 'dev fts5' ./... && GOOS=windows go vet ./... || die "build/vet failed"; echo build-vet-ok
CLOUD_GOLDEN_UPDATE=1 go test -count=1 -run TestCloudRouteCensus ./internal/server/ >/dev/null 2>&1
UPDATE_GOLDEN=1 go test -count=1 -run TestRouteBindingsAreReviewed ./internal/server/routes/ >/dev/null 2>&1
(cd .. && python3 tools/gen_route_queries.py >/dev/null 2>&1)
[ -z "$(git status --porcelain)" ] || { git status --porcelain; die "regenerators left a diff: regenerate and commit"; }; echo regen-ok
TMPDIR=$T go test -count=1 -p 4 ./... > "$LOGS/gotest-default.log" 2>&1 || die "go test failed: $LOGS/gotest-default.log"; echo gotest-ok
TMPDIR=$T2 go test -count=1 -p 4 -tags 'dev fts5' ./... > "$LOGS/gotest-dev.log" 2>&1 || die "go test (dev fts5) failed: $LOGS/gotest-dev.log"; echo gotest-dev-ok
cd "$W/tests/frontend" || exit 1
[ -e node_modules ] || ln -s "$MAIN_TREE/tests/frontend/node_modules" node_modules
npm run -s test:cloud > "$LOGS/testcloud.log" 2>&1 || die "test:cloud failed: $LOGS/testcloud.log"; echo testcloud-ok
echo "prepush: all green on $(git -C "$W" rev-parse --short HEAD)"
