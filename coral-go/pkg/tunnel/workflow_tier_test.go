package tunnel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The operator's rule of 2026-10-23 (16:20Z): a view-only Coral Hub viewer sees no workflow at all. Every workflow route that reads (the list, one by id or name, a workflow's runs, the
// recent runs, one run) needs Interact; none of them is a View route, and the ones that act keep their own higher tier.
func TestNoWorkflowRouteIsReadableAtView(t *testing.T) {
	for _, p := range []string{
		"/api/workflows", "/api/workflows/7", "/api/workflows/by-name/nightly", "/api/workflows/7/runs", "/api/workflows/runs/recent", "/api/workflows/runs/12",
	} {
		assert.Equal(t, TierInteract, Classify("GET", p, ""), "GET %s reads a workflow: Interact", p)
	}
	for _, rule := range RoutePatterns() {
		f := strings.Fields(rule)
		if len(f) == 3 && strings.Contains(f[1], "/api/workflows") {
			assert.NotEqual(t, "view", f[2], "a workflow route must never be a View route: %s", rule)
		}
	}
	assert.Equal(t, TierLaunch, Classify("POST", "/api/workflows/7/trigger", ""))
	assert.Equal(t, TierInteract, Classify("POST", "/api/workflows/runs/12/kill", ""))
	assert.Equal(t, TierNever, Classify("POST", "/api/workflows", ""))
	assert.Equal(t, TierNever, Classify("GET", "/api/workflows/runs/12/steps/0/files/out.txt", ""))
}
