package querypage

import (
	"testing"
)

func TestHasControl(t *testing.T) {
	cases := ControlCases()
	if len(cases) != 3*(0x20+2) {
		t.Fatalf("the cases: %d", len(cases))
	}
	for _, s := range cases {
		if !HasControl(s) {
			t.Errorf("%q has a control character", s)
		}
	}
	for _, s := range []string{"", "ab", " a b ", "résumé", "日本語", "emoji 👍", "tab-free: a-b_c", "100% a_b"} {
		if HasControl(s) {
			t.Errorf("%q has none", s)
		}
	}
}
