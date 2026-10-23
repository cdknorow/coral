//go:build !windows

package ptymanager

import (
	"strings"
	"testing"
)

// The guardian ends every registered terminal's tree when Coral is gone, so what it registers from a line on its pipe is checked here without ever being registered: pid 1 is init, pid 0 and a negative pid
// reach process groups. (QA's mutation audit of 159, G8: the guard could not be tested through a real guardian, which a mutant would have made end the user's whole session.)
func TestParseGuardianAddRefusesWhatMustNeverBeRegistered(t *testing.T) {
	for _, c := range []struct {
		line   string
		pid    int
		tag    string
		wantOK bool
	}{
		{"add 4242 1700000000000 tag-a", 4242, "tag-a", true},
		{"add 2 5 t", 2, "t", true},
		{"add 1 1700000000000 tag", 0, "", false},                    // init
		{"add 0 1700000000000 tag", 0, "", false},                    // the caller's process group
		{"add -1 1700000000000 tag", 0, "", false},                   // every process
		{"add -4242 1700000000000 tag", 0, "", false},                // a process group
		{"add abc 1700000000000 tag", 0, "", false},                  // not a number
		{"add 4242x 1700000000000 tag", 0, "", false},                // not a number
		{"add 4242 notatime tag", 0, "", false},                      // no start time
		{"add 4242 1700000000000", 0, "", false},                     // no tag
		{"add 4242 1700000000000 tag extra", 0, "", false},           // too many fields
		{"del 4242 1700000000000 tag", 0, "", false},                 // not an add
		{"add 99999999999999999999 1700000000000 tag", 0, "", false}, // overflows an int
		{"", 0, "", false},
	} {
		root, tag, ok := parseGuardianAdd(strings.Fields(c.line))
		if ok != c.wantOK || (ok && (root.pid != c.pid || tag != c.tag)) {
			t.Errorf("%q: got pid %d tag %q ok %v, want pid %d tag %q ok %v", c.line, root.pid, tag, ok, c.pid, c.tag, c.wantOK)
		}
		if !ok && (root != member{} || tag != "") {
			t.Errorf("%q: a refused line gives nothing back, got %v %q", c.line, root, tag)
		}
	}
	if root, _, ok := parseGuardianAdd(strings.Fields("add 77 1234 t")); !ok || root.created != 1234 {
		t.Errorf("the start time is kept: %v %v", root, ok)
	}
}
