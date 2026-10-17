package tunnel

import (
	"strings"
	"testing"
)

// Every entry of the query table is a route the tunnel lets through at some tier, and the
// rule that classifies it is the one the entry is keyed by (#4148, finding 5 of the #3901
// allowlist review). The generator filters by ShapeAllowed (#3942), but its drift test is
// skipped without python3; this checks the committed table itself, so a hand edit or a later
// tier change that leaves a dead entry fails here.
func TestRouteQueriesListOnlyReachableRoutes(t *testing.T) {
	if len(routeQueries) == 0 {
		t.Fatal("routeQueries is empty")
	}
	for shape := range routeQueries {
		method, path, ok := strings.Cut(shape, " ")
		if !ok {
			t.Errorf("%q: not \"METHOD /path\"", shape)
			continue
		}
		if !ShapeAllowed(method, path) {
			t.Errorf("%s: the tunnel refuses this route at every tier; drop it from the table (python3 tools/gen_route_queries.py)", shape)
			continue
		}
		pattern, tier, ok := RuleFor(method, strings.ReplaceAll(path, "{}", shapeSegment))
		if !ok || tier == TierNever {
			t.Errorf("%s: no allowing rule", shape)
			continue
		}
		if got := shapeOf(method, pattern); got != shape {
			t.Errorf("%s: classified by %s, so this entry's parameters are never consulted", shape, got)
		}
	}
}
