package tmux

import "testing"

// #4057: the bare-name warning memo is forgotten when the session ends, on every backend.
func TestForgetBareNameWarning(t *testing.T) {
	size := func() int {
		n := 0
		bareNameWarned.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	start := size()
	for i := 0; i < 50; i++ {
		name := "claude-4057-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		warnBareNameLookup("tmux", name)
		WarnBareNameLookup("pty", name)
	}
	if size() != start+100 {
		t.Fatalf("setup: %d entries", size()-start)
	}
	for i := 0; i < 50; i++ {
		ForgetBareNameWarning("claude-4057-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if got := size(); got != start {
		t.Fatalf("after the sessions ended: %d entries, want %d", got, start)
	}
}
