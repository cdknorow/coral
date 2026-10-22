package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestExitItemsFollowTheBackend(t *testing.T) {
	for _, c := range []struct {
		goos, backend string
		oneExit       bool
		quit          string
	}{
		{"darwin", "pty", true, "Quit Coral"},
		{"linux", "pty", true, "Quit Coral"},
		{"darwin", "", true, "Quit Coral"}, // before the server answers: the default
		{"windows", "pty", true, "Quit Coral"},
		{"windows", "tmux", true, "Quit Coral"},
		{"darwin", "tmux", false, "Quit Coral"},
		{"linux", "tmux", false, "Quit Coral"},
	} {
		got := exitItemsFor(c.goos, c.backend, 0)
		if got.oneExit != c.oneExit || got.quit != c.quit {
			t.Errorf("%s/%s: %+v", c.goos, c.backend, got)
		}
		if got.oneExit && got.quitTip != "Exit Coral" {
			t.Errorf("%s/%s: no agents running, nothing to say about them: %q", c.goos, c.backend, got.quitTip)
		}
	}
	if tmux := exitItemsFor("linux", "tmux", 5); tmux.quit != "Quit Coral" || tmux.quitTip != "Exit Coral. The agents keep running (tmux terminal backend)." || tmux.shutdown != "Quit and stop all agents" {
		t.Errorf("tmux keeps its two exits and says nothing about waking: %+v", tmux)
	}
}

// On the built-in terminal Quit names the agents and says they wake next time; 1 is singular (#6131).
func TestPtyQuitNamesTheAgents(t *testing.T) {
	for _, c := range []struct {
		agents    int
		quit, tip string
		note      string
	}{
		{0, "Quit Coral", "Exit Coral", "Coral quit."},
		{1, "Quit Coral (1 agent wakes next time)", "1 agent stops now and wakes next time you open Coral", "Coral quit. 1 agent will wake next time you open Coral."},
		{3, "Quit Coral (3 agents wake next time)", "3 agents stop now and wake next time you open Coral", "Coral quit. 3 agents will wake next time you open Coral."},
	} {
		for _, goos := range []string{"darwin", "linux", "windows"} {
			got := exitItemsFor(goos, "pty", c.agents)
			if !got.oneExit || got.quit != c.quit || got.quitTip != c.tip || got.shutdown != c.quit {
				t.Errorf("%s, %d agents: %+v", goos, c.agents, got)
			}
		}
		if got := quitNote(c.agents); got != c.note {
			t.Errorf("quitNote(%d) = %q", c.agents, got)
		}
	}
}

func TestRunningAgentsCountsTheAwakeOnes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/live" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `[{"name":"a","sleeping":false},{"name":"b","sleeping":true},{"name":"c"},{"name":"d","sleeping":false}]`)
	}))
	defer srv.Close()
	if got := runningAgents(srv.Client(), srv.URL); got != 3 {
		t.Errorf("3 awake of 4 rows: %d", got)
	}
	if got := runningAgents(srv.Client(), "http://127.0.0.1:1"); got != 0 {
		t.Errorf("no server: %d", got)
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

// Quit on the built-in terminal stops the server and ends no agent itself: the only requests are reads (#6131).
func TestPtyQuitDoesNotKillAgents(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, `[{"name":"a","sleeping":false},{"name":"b","sleeping":false}]`)
	}))
	defer srv.Close()
	var steps []string
	quitKeepingAgentsPlace(srv.URL, t.TempDir(),
		func() { steps = append(steps, "close app") },
		func() { steps = append(steps, "stop server") },
		func(m string) { steps = append(steps, m) },
		func() { steps = append(steps, "exit") })
	if want := "close app|stop server|Coral quit. 2 agents will wake next time you open Coral.|exit"; strings.Join(steps, "|") != want {
		t.Errorf("steps: %q", steps)
	}
	for _, r := range seen {
		if r != "GET /api/sessions/live" {
			t.Errorf("Quit sent %q: it must not end agents", r)
		}
	}
}
