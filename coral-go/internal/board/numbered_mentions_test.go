package board

import "testing"

// #3543: "@Backend Dev 2" tags the numbered agent "Backend Dev 2" and not "Backend Dev", but
// only on a board that HAS a "Backend Dev 2" (Security's review): elsewhere "@Orchestrator 3
// tasks are ready" still tags the Orchestrator.
func TestNumberedNameMentions(t *testing.T) {
	board := map[string]bool{"backend dev 2": true, "backend dev 12": true}
	for _, c := range []struct {
		content, sub string
		numbered     map[string]bool
		want         bool
	}{
		{"@Backend Dev 2 please take #7", "Backend Dev 2", board, true},
		{"@Backend Dev 2 please take #7", "Backend Dev", board, false},
		{"@Backend Dev and @Backend Dev 2 sync up", "Backend Dev", board, true},
		{"@Backend Dev and @Backend Dev 2 sync up", "Backend Dev 2", board, true},
		{"@Backend Dev 2", "Backend Dev", board, false},
		{"@Backend Dev, please", "Backend Dev", board, true},
		{"@Backend Dev 2nd pass", "Backend Dev", board, true},
		{"@BackendDev2 hi", "Backend Dev", board, false},
		{"@Backend Dev 12, hi", "Backend Dev 1", board, false},
		{"@Backend Dev 12, hi", "Backend Dev 12", board, true},
		// No such numbered agent: the base name is tagged.
		{"@Backend Dev 3 files changed", "Backend Dev", board, true},
		{"@Orchestrator 3 tasks are ready", "Orchestrator", board, true},
		{"@Orchestrator 3 tasks are ready", "Orchestrator", nil, true},
		{"@Backend Dev 2 please", "Backend Dev", nil, true},
	} {
		if got := containsExplicitTag(c.content, c.sub, "", c.numbered); got != c.want {
			t.Errorf("containsExplicitTag(%q, %q) = %v, want %v", c.content, c.sub, got, c.want)
		}
	}
}
