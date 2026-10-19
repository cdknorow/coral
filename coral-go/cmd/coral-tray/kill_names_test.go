package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Shutdown kills every agent by the name the server lists it under, and that name is a folder name (#5094): a space, "?", "#", "%", "/" or a control byte in it
// must reach the server as that one name (escaped), never as another route or a query. Before, an unescaped name with a control byte or a bad escape made the
// request unbuildable and the ignored error a nil pointer panic that stopped the Shutdown action before it stopped the server.
func TestKillAllAgentsEscapesEveryName(t *testing.T) {
	names := []string{"plain", "My Project", "a/b", "q?x=1", "h#frag", "pct%41", "ctl\x01name", "last"}
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/live" {
			var rows []map[string]any
			for i, n := range names {
				rows = append(rows, map[string]any{"name": n, "session_id": "sid", "agent_type": "claude", "i": i})
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		mu.Lock()
		got = append(got, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	killed := killAllAgents(srv.URL, t.TempDir())
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/api/sessions/live/plain/kill?", "/api/sessions/live/My%20Project/kill?", "/api/sessions/live/a%2Fb/kill?", "/api/sessions/live/q%3Fx=1/kill?",
		"/api/sessions/live/h%23frag/kill?", "/api/sessions/live/pct%2541/kill?", "/api/sessions/live/ctl%01name/kill?", "/api/sessions/live/last/kill?",
	}
	if len(got) != len(want) {
		t.Fatalf("kill requests %q, want %q (killed=%d)", got, want, killed)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d: %q, want %q", i, got[i], want[i])
		}
	}
	if killed != len(want) {
		t.Errorf("killed=%d, want %d: every agent was reached, whatever its name", killed, len(want))
	}
}
