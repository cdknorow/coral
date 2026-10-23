package main

import (
	"encoding/json"
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
		got := exitItemsFor(c.goos, c.backend, 0, true)
		if got.oneExit != c.oneExit || got.quit != c.quit {
			t.Errorf("%s/%s: %+v", c.goos, c.backend, got)
		}
		if got.oneExit && got.quitTip != "Exit Coral" {
			t.Errorf("%s/%s: no agents running, nothing to say about them: %q", c.goos, c.backend, got.quitTip)
		}
	}
	if tmux := exitItemsFor("linux", "tmux", 5, true); tmux.quit != "Quit Coral" || tmux.quitTip != "Exit Coral. The agents keep running (tmux terminal backend)." || tmux.shutdown != "Quit and stop all agents" {
		t.Errorf("tmux keeps its two exits and says nothing about waking: %+v", tmux)
	}
}

// On the built-in terminal Quit names the agents and says what happens next: they wake (setting on, within the cap) or they stop (setting off, or over the cap); 1 is singular (#6131).
func TestPtyQuitNamesTheAgents(t *testing.T) {
	for _, c := range []struct {
		name      string
		agents    int
		wakes     bool
		quit, tip string
		note      string
	}{
		{"none", 0, true, "Quit Coral", "Exit Coral", "Coral quit."},
		{"none, setting off", 0, false, "Quit Coral", "Exit Coral", "Coral quit."},
		{"on, 1", 1, true, "Quit Coral (1 agent wakes next time)", "1 agent stops now and wakes next time you open Coral", "Coral quit. 1 agent will wake next time you open Coral."},
		{"on, 3", 3, true, "Quit Coral (3 agents wake next time)", "3 agents stop now and wake next time you open Coral", "Coral quit. 3 agents will wake next time you open Coral."},
		{"off, 1", 1, false, "Quit Coral (1 agent stops)", "1 agent stops now. Wake it from Coral when you want it back.", "Coral quit. 1 agent stopped. Wake it from Coral when you want it back."},
		{"off or over the cap, 3", 3, false, "Quit Coral (3 agents stop)", "3 agents stop now. Wake them from Coral when you want them back.", "Coral quit. 3 agents stopped. Wake them from Coral when you want them back."},
	} {
		for _, goos := range []string{"darwin", "linux", "windows"} {
			got := exitItemsFor(goos, "pty", c.agents, c.wakes)
			if !got.oneExit || got.quit != c.quit || got.quitTip != c.tip || got.shutdown != c.quit {
				t.Errorf("%s, %s: %+v", c.name, goos, got)
			}
		}
		if got := quitNote(c.agents, c.wakes); got != c.note {
			t.Errorf("%s: quitNote = %q", c.name, got)
		}
	}
}

// "Wake next time" is said only for what will wake: the setting on, and no more than the cap running (above it Coral asks first).
func TestWakeRule(t *testing.T) {
	for _, c := range []struct {
		body   string
		agents int
		wakes  bool
	}{
		{`{"settings":{}}`, 3, true}, // never saved: on, up to 8
		{`{"settings":{}}`, 8, true},
		{`{"settings":{}}`, 9, false},
		{`{"settings":{"auto_wake_after_restart":"true","auto_wake_cap":"2"}}`, 2, true},
		{`{"settings":{"auto_wake_after_restart":"true","auto_wake_cap":"2"}}`, 3, false}, // over the cap
		{`{"settings":{"auto_wake_after_restart":"false"}}`, 1, false},                    // off
		{`{"settings":{"auto_wake_cap":"99"}}`, 9, false},                                 // an odd cap is the default 8
		{`not json`, 3, true},
	} {
		r, _ := wakeRuleFrom([]byte(c.body))
		if got := r.wakes(c.agents); got != c.wakes {
			t.Errorf("%s with %d agents: wakes = %v, want %v", c.body, c.agents, got, c.wakes)
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
		if r.URL.Path == "/api/settings" {
			io.WriteString(w, `{"settings":{"auto_wake_after_restart":"true"}}`)
			return
		}
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
		if r != "GET /api/sessions/live" && r != "GET /api/settings" && r != "GET /api/system/auto-wake" {
			t.Errorf("Quit sent %q: it must not end agents", r)
		}
	}
}

// When the setting cannot be read the tray does not promise waking: "stop", never "wake next time". Readable with the keys absent is the default (on, up to 8): "wake" (#6131).
func TestUnreadableWakeSettingSaysStop(t *testing.T) {
	for _, c := range []struct {
		name    string
		handler http.HandlerFunc
		wakes   bool
	}{
		{"500", func(w http.ResponseWriter, r *http.Request) { settingsOr(w, r, func() { http.Error(w, "boom", 500) }) }, false},
		{"401", func(w http.ResponseWriter, r *http.Request) {
			settingsOr(w, r, func() { http.Error(w, "sign in", 401) })
		}, false},
		{"not json", func(w http.ResponseWriter, r *http.Request) {
			settingsOr(w, r, func() { io.WriteString(w, "not json") })
		}, false},
		{"connection closed", func(w http.ResponseWriter, r *http.Request) {
			settingsOr(w, r, func() {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			})
		}, false},
		{"settings with no keys", func(w http.ResponseWriter, r *http.Request) {
			settingsOr(w, r, func() { io.WriteString(w, `{"settings":{}}`) })
		}, true},
	} {
		srv := httptest.NewServer(c.handler)
		got := readAgentsNow(srv.URL, t.TempDir())
		srv.Close()
		if got.count != 3 || got.wakes != c.wakes {
			t.Errorf("%s: %+v, want 3 agents, wakes %v", c.name, got, c.wakes)
		}
		it := exitItemsFor("darwin", "pty", got.count, got.wakes)
		want := "Quit Coral (3 agents stop)"
		note := "Coral quit. 3 agents stopped. Wake them from Coral when you want them back."
		if c.wakes {
			want, note = "Quit Coral (3 agents wake next time)", "Coral quit. 3 agents will wake next time you open Coral."
		}
		if it.quit != want || quitNote(got.count, got.wakes) != note {
			t.Errorf("%s: %q / %q", c.name, it.quit, quitNote(got.count, got.wakes))
		}
	}
}

// settingsOr answers the live list with three running agents and hands /api/settings to settings.
func settingsOr(w http.ResponseWriter, r *http.Request, settings func()) {
	if r.URL.Path == "/api/settings" {
		settings()
		return
	}
	io.WriteString(w, `[{"name":"a","sleeping":false},{"name":"b","sleeping":false},{"name":"c","sleeping":false}]`)
}

// A Quit that follows a wake by less than 10 minutes is skipped by the server's crash-loop guard: the label and the note say that, in the server's words, and not "wake next time" (#319).
func TestGuardedWakeSaysItWillNotWake(t *testing.T) {
	const why = "Coral woke its agents less than 10 minutes ago"
	for _, c := range []struct {
		name     string
		settings string
		guard    string
		agents   int
		quit     string
		note     string
		wakes    bool
	}{
		{"guarded", `{"settings":{}}`, `{"state":"done","guarded_now":true,"guard_why":"` + why + `"}`, 3,
			"Quit Coral (3 agents stop: " + why + ")", "Coral quit. 3 agents stopped: " + why + ". Wake them from Coral when you want them back.", false},
		{"guarded, one agent", `{"settings":{}}`, `{"guarded_now":true,"guard_why":"` + why + `"}`, 1,
			"Quit Coral (1 agent stops: " + why + ")", "Coral quit. 1 agent stopped: " + why + ". Wake it from Coral when you want it back.", false},
		{"not guarded", `{"settings":{}}`, `{"state":"","guarded_now":false}`, 3,
			"Quit Coral (3 agents wake next time)", "Coral quit. 3 agents will wake next time you open Coral.", true},
		{"an older server says nothing about it", `{"settings":{}}`, `{"state":"done"}`, 3,
			"Quit Coral (3 agents wake next time)", "Coral quit. 3 agents will wake next time you open Coral.", true},
		{"the setting is off: stop, and no guard reason", `{"settings":{"auto_wake_after_restart":"false"}}`, `{"guarded_now":true,"guard_why":"` + why + `"}`, 3,
			"Quit Coral (3 agents stop)", "Coral quit. 3 agents stopped. Wake them from Coral when you want them back.", false},
		{"the guard cannot be read: the setting decides", `{"settings":{}}`, ``, 3,
			"Quit Coral (3 agents wake next time)", "Coral quit. 3 agents will wake next time you open Coral.", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/settings":
				io.WriteString(w, c.settings)
			case "/api/system/auto-wake":
				if c.guard == "" {
					http.Error(w, "boom", 500)
					return
				}
				io.WriteString(w, c.guard)
			default:
				io.WriteString(w, `[{"name":"a","sleeping":false},{"name":"b","sleeping":false},{"name":"c","sleeping":false}]`)
			}
		}))
		got := readAgentsNow(srv.URL, t.TempDir())
		srv.Close()
		n := got.count
		if c.agents == 1 { // the same server, one agent: only the wording is under test
			n = 1
		}
		it := exitItemsWhy("windows", "pty", n, got.wakes, got.why)
		if it.quit != c.quit || quitNoteWhy(n, got.wakes, got.why) != c.note || got.wakes != c.wakes {
			t.Errorf("%s: wakes=%v %q / %q", c.name, got.wakes, it.quit, quitNoteWhy(n, got.wakes, got.why))
		}
		if !c.wakes && got.why != "" && !strings.Contains(it.quitTip, why) {
			t.Errorf("%s: the tooltip does not say why: %q", c.name, it.quitTip)
		}
	}
}

// Security's note on #144: the guard's words come from the HTTP answer and go into a menu title, so they are cleaned and capped.
func TestTheGuardWordsAreCleanedAndCapped(t *testing.T) {
	long := strings.Repeat("x", 300)
	for name, tc := range map[string]struct{ in, want string }{
		"control characters and a line break": {"Coral woke\x1b its agents\r\nless than 10 minutes ago", ""},
		"a very long sentence":                {long, long[:100]},
		"the real sentence":                   {"Coral woke its agents less than 10 minutes ago", "Coral woke its agents less than 10 minutes ago"},
	} {
		body, _ := json.Marshal(map[string]any{"guarded_now": true, "guard_why": tc.in})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		guarded, why := wakeGuardNow(srv.Client(), srv.URL)
		srv.Close()
		if !guarded {
			t.Errorf("%s: not guarded", name)
		}
		if strings.ContainsAny(why, "\x1b\r\n") || len([]rune(why)) > 100 {
			t.Errorf("%s: %q", name, why)
		}
		if tc.want != "" && why != tc.want {
			t.Errorf("%s: %q, want %q", name, why, tc.want)
		}
	}
}
