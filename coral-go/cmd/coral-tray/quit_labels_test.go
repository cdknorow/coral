package main

import "testing"

func TestExitItemsFollowTheBackend(t *testing.T) {
	for _, c := range []struct {
		goos, backend string
		oneExit       bool
		quit          string
	}{
		{"darwin", "pty", true, "Quit and stop all agents"},
		{"linux", "pty", true, "Quit and stop all agents"},
		{"darwin", "", true, "Quit and stop all agents"}, // before the server answers: the default
		{"windows", "pty", true, "Quit and stop all agents"},
		{"windows", "tmux", true, "Quit and stop all agents"},
		{"darwin", "tmux", false, "Quit Coral"},
		{"linux", "tmux", false, "Quit Coral"},
	} {
		got := exitItemsFor(c.goos, c.backend)
		if got.oneExit != c.oneExit || got.quit != c.quit {
			t.Errorf("%s/%s: %+v", c.goos, c.backend, got)
		}
		if got.oneExit && got.quitTip != "Stop all agents and exit Coral" {
			t.Errorf("%s/%s: one exit that says agents keep running: %q", c.goos, c.backend, got.quitTip)
		}
	}
	if tmux := exitItemsFor("linux", "tmux"); tmux.quitTip != "Exit Coral. The agents keep running (tmux terminal backend)." || tmux.shutdown != "Quit and stop all agents" {
		t.Errorf("tmux keeps its two exits: %+v", tmux)
	}
}

func TestRunningBackend(t *testing.T) {
	for in, want := range map[string]string{
		`{"running":"pty","saved":""}`: "pty",
		`{"running":"tmux"}`:           "tmux",
		`{"running":"screen"}`:         "",
		`{"saved":"tmux"}`:             "",
		`not json`:                     "",
		``:                             "",
		`{"running":"PTY"}`:            "",
	} {
		if got := runningBackend([]byte(in)); got != want {
			t.Errorf("runningBackend(%q) = %q, want %q", in, got, want)
		}
	}
}
