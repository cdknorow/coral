package backendchoice

import "strings"

import "testing"

func TestResolve(t *testing.T) {
	none := func() (int, bool) { return 0, true }
	some := func() (int, bool) { return 3, true }
	unknown := func() (int, bool) { return 0, false } // the server may be there and could not be asked
	boom := func() (int, bool) {
		t.Helper()
		t.Error("the private tmux server was asked when a choice was already made")
		return 0, true
	}
	for _, c := range []struct {
		name             string
		flag, saved, os  string
		count            func() (int, bool)
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
		{"--backend tmux on Windows is not honoured", "tmux", "", "windows", boom, "pty", false, false},
		{"--backend TMUX on Windows is not honoured", "TMUX", "pty", "windows", boom, "pty", false, false},
		{"--backend pty on Windows", "pty", "tmux", "windows", boom, "pty", true, false},
		// the exception: never hide agents that are running
		{"no choice, agents in tmux: this start runs tmux", "", "", "darwin", some, "tmux", false, true},
		{"no choice, agents in tmux, Linux", "", "", "linux", some, "tmux", false, true},
		{"no choice, none left: pty", "", "", "darwin", none, "pty", false, false},
		{"an unknown saved value with agents in tmux", "", "screen", "linux", some, "tmux", false, true},
		{"saved pty wins over agents in tmux", "", "pty", "darwin", boom, "pty", false, false},
		{"no counter given", "", "", "linux", nil, "pty", false, false},
		// the server could not be asked: never taken for "none"
		{"no choice, the server could not be asked", "", "", "darwin", unknown, "tmux", false, true},
		{"the same on Linux", "", "", "linux", unknown, "tmux", false, true},
		{"a saved pty choice is kept even when the server could not be asked", "", "pty", "darwin", boom, "pty", false, false},
		{"--backend pty wins over an unknown", "pty", "", "linux", boom, "pty", true, false},
		{"Windows never asks", "", "", "windows", boom, "pty", false, false},
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
	count := func() (int, bool) { return running, true }
	if b, _, kept := Resolve("", "", "darwin", count); b != "tmux" || !kept {
		t.Fatalf("first start: %s kept=%v", b, kept)
	}
	running = 0 // the agents ended; nothing was saved
	if b, _, kept := Resolve("", "", "darwin", count); b != "pty" || kept {
		t.Fatalf("next start: %s kept=%v", b, kept)
	}
}

// A forced tmux is refused on Windows with one line; everything else is allowed (#304).
func TestFlagProblem(t *testing.T) {
	for _, c := range []struct {
		goos, flag string
		refused    bool
	}{
		{"windows", "tmux", true},
		{"windows", "TMUX", true},
		{"windows", " tmux ", true},
		{"windows", "pty", false},
		{"windows", "", false},
		{"linux", "tmux", false},
		{"darwin", "tmux", false},
	} {
		msg := FlagProblem(c.goos, c.flag)
		if c.refused {
			if !strings.Contains(msg, "tmux isn't supported on Windows") || !strings.Contains(msg, "its own terminals") {
				t.Errorf("%s %q: %q", c.goos, c.flag, msg)
			}
		} else if msg != "" {
			t.Errorf("%s %q: unexpected %q", c.goos, c.flag, msg)
		}
	}
}
