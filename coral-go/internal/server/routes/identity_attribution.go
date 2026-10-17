package routes

import (
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// identity_actor (#4255, identity phase 2): "log" (the default) leaves every decision as it was and
// counts, per site and route, where deciding by the actor alone would decide differently; "enforce"
// decides by the actor alone (the member name or the Operator the identity layer decided, never what
// the request claims, and nothing privileged accepts a claimed actor). It is turned on only after the
// identity harness has run clean for a release and the Operator says so; the switch is the Operator's
// (set only through the Operator-only setting, never by an agent, never through Coral Hub).
var actorEnforced atomic.Bool

func identityActorEnforced() bool { return actorEnforced.Load() }

// setIdentityActorEnforced is the one place the mode changes (the Operator's setting, and tests).
func setIdentityActorEnforced(on bool) { actorEnforced.Store(on) }

// enforceDiffers counts decisions that enforce would make differently from log mode, by "site
// METHOD /route" (a site with no request, such as a background rule, counts under its own key). It is
// process-wide, like the other identity counters, and bounded.
var enforceDiffers = struct {
	mu sync.Mutex
	m  map[string]int64
}{m: map[string]int64{}}

func noteEnforceDiffersKey(key string) {
	enforceDiffers.mu.Lock()
	defer enforceDiffers.mu.Unlock()
	if _, ok := enforceDiffers.m[key]; !ok && len(enforceDiffers.m) >= identityBreakKeysMax {
		key = "other"
	}
	enforceDiffers.m[key]++
}

// noteEnforceDiffers records, in log mode, that enforce would have decided request r differently at site.
func noteEnforceDiffers(site string, r *http.Request) {
	noteEnforceDiffersKey(site + " " + r.Method + " " + routeOf(r))
}

// enforceDiffersSnapshot is the counters, for the identity report.
func enforceDiffersSnapshot() map[string]int64 {
	enforceDiffers.mu.Lock()
	defer enforceDiffers.mu.Unlock()
	out := make(map[string]int64, len(enforceDiffers.m))
	for k, v := range enforceDiffers.m {
		out[k] = v
	}
	return out
}

// enforceDiffersSites is the sorted keys of the counters (tests).
func enforceDiffersSites() []string {
	snap := enforceDiffersSnapshot()
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resetEnforceDiffers clears the counters (tests).
func resetEnforceDiffers() {
	enforceDiffers.mu.Lock()
	enforceDiffers.m = map[string]int64{}
	enforceDiffers.mu.Unlock()
}

// actorMode is the mode's name, for the identity report.
func actorMode() string {
	if identityActorEnforced() {
		return "enforce"
	}
	return "log"
}
