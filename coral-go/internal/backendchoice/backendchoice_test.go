package backendchoice

import "testing"

func TestResolve(t *testing.T) {
	none := func() int { return 0 }
	some := func() int { return 3 }
	boom := func() int { t.Helper(); t.Error("the private tmux server was asked when a choice was already made"); return 0 }
	for _, c := range []struct {
		name             string
		flag, saved, os  string
		count            func() int
		want             string
		forced, wantKept bool
	}{
		{"new install, macOS", "", "", "darwin", none, "pty", false, false},
		{"new install, Linux", "", "", "linux", none, "pty", false, false},
		{"Windows", "", "", "windows", boom, "pty", false, false},
		{"Windows ignores a saved tmux", "", "tmux", "windows", boom, "pty", false, false},
		{"saved pty", "", "pty", "darwin", boom, "pty", false, false},
		{"saved tmux is kept", "", "tmux", "linux", boom, "tmux", false, false},
		{"an unreadable saved value is no choice", "", "PTY", "darwin", none, "pty", false, false},
		{"an unknown saved value is no choice", "", "screen", "linux", none, "pty", false, false},
		{"--backend pty", "pty", "tmux", "darwin", boom, "pty", true, false},
		{"--backend tmux", "tmux", "pty", "linux", boom, "tmux", true, false},
		{"--backend tmux on Windows", "tmux", "", "windows", boom, "tmux", true, false},
		// the exception: never hide agents that are running
		{"no choice, agents in tmux: this start runs tmux", "", "", "darwin", some, "tmux", false, true},
		{"no choice, agents in tmux, Linux", "", "", "linux", some, "tmux", false, true},
		{"no choice, none left: pty", "", "", "darwin", none, "pty", false, false},
		{"an unknown saved value with agents in tmux", "", "screen", "linux", some, "tmux", false, true},
		{"saved pty wins over agents in tmux", "", "pty", "darwin", boom, "pty", false, false},
		{"no counter given", "", "", "linux", nil, "pty", false, false},
	} {
		got, forced, kept := Resolve(c.flag, c.saved, c.os, c.count)
		if got != c.want || forced != c.forced || kept != c.wantKept {
			t.Errorf("%s: Resolve(%q, %q, %q) = %q, forced=%v, kept=%v; want %q, %v, %v", c.name, c.flag, c.saved, c.os, got, forced, kept, c.want, c.forced, c.wantKept)
		}
	}
}

// Two starts, as an upgraded install sees them: the first with agents still in tmux, the next without.
func TestAnUpgradedInstallRunsTmuxOnceThenPty(t *testing.T) {
	running := 2
	count := func() int { return running }
	if b, _, kept := Resolve("", "", "darwin", count); b != "tmux" || !kept {
		t.Fatalf("first start: %s kept=%v", b, kept)
	}
	running = 0 // the agents ended; nothing was saved
	if b, _, kept := Resolve("", "", "darwin", count); b != "pty" || kept {
		t.Fatalf("next start: %s kept=%v", b, kept)
	}
}
