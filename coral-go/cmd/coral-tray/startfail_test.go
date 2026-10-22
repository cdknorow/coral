package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// #4151: the tray's error menu says why the server did not start, in one short line.
func TestStartFailureTitle(t *testing.T) {
	for in, want := range map[string]string{
		"cannot start Coral: another Coral server is already running for this data directory (stop the existing server or use a different data directory)": "Not started: another Coral server is already running for this data directory (stop…",
		"can't listen on 127.0.0.1 port 8420 (in use?): listen tcp 127.0.0.1:8420: bind: address already in use":                                           "Not started: can't listen on 127.0.0.1 port 8420 (in use?): listen tcp 127.0.0.1:8…",
		"failed to open database\nsecond line": "Not started: failed to open database",
		"Coral is already running on this data folder (C:\\Users\\me\\.coral), at http://127.0.0.1:8450, PID 1234. Open it with 'coral open'.": "Not started: Coral is already running on this data folder (C:\\Users\\me\\.coral), at…", // #279: no "cannot start Coral:" lead before this one now
	} {
		got := statusTitle("Not started: ", in)
		if got != want {
			t.Errorf("statusTitle(%q)\n got %q\nwant %q", in, got, want)
		}
		if n := utf8.RuneCountInString(strings.TrimPrefix(got, "Not started: ")); n > 70 {
			t.Errorf("%q: %d characters", got, n)
		}
	}
}
