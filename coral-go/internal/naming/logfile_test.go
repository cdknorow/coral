package naming

import (
	"path/filepath"
	"testing"
)

// #3602 W14, W15: a log path is built only from an agent type of [A-Za-z0-9_] and a session id
// that is one safe path element; anything else gives "" so no file is touched outside logDir.
func TestLogFileRefusesUnsafeParts(t *testing.T) {
	dir := t.TempDir()
	const id = "0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11"
	if got := LogFile(dir, "claude", id); got != filepath.Join(dir, "claude_coral_"+id+".log") {
		t.Fatalf("a normal log path: %q", got)
	}
	for _, c := range []struct{ typ, sid string }{
		{"../x", id}, {"a/b", id}, {"cla-ude", id}, {"", id}, {"claude", "../x"}, {"claude", "a/b"},
		{"claude", ".."}, {"claude", "-x"}, {"claude", ""}, {`c\d`, id},
	} {
		if got := LogFile(dir, c.typ, c.sid); got != "" {
			t.Errorf("LogFile(%q, %q) = %q, want \"\"", c.typ, c.sid, got)
		}
	}
}
