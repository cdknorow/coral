package board

import "testing"

// #3543: "@Backend Dev 2" tags the numbered agent "Backend Dev 2" and not "Backend Dev";
// everything else about mentions is unchanged.
func TestNumberedNameMentions(t *testing.T) {
	for _, c := range []struct {
		content, sub string
		want         bool
	}{
		{"@Backend Dev 2 please take #7", "Backend Dev 2", true},
		{"@Backend Dev 2 please take #7", "Backend Dev", false},
		{"@Backend Dev and @Backend Dev 2 sync up", "Backend Dev", true},
		{"@Backend Dev and @Backend Dev 2 sync up", "Backend Dev 2", true},
		{"@Backend Dev 2", "Backend Dev", false},
		{"@Backend Dev, please", "Backend Dev", true},
		{"@Backend Dev 2nd pass", "Backend Dev", true},
		{"@BackendDev2 hi", "Backend Dev", false},
		{"@Backend Dev 12, hi", "Backend Dev 1", false},
		{"@Backend Dev 12, hi", "Backend Dev 12", true},
	} {
		if got := containsExplicitTag(c.content, c.sub, ""); got != c.want {
			t.Errorf("containsExplicitTag(%q, %q) = %v, want %v", c.content, c.sub, got, c.want)
		}
	}
}
