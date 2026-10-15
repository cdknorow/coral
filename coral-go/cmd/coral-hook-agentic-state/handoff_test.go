package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// #3481 phase 1B: Claude's Stop hook asks Coral whether to hand over board messages; a
// hand-off becomes Claude's block output (a short reason, the instruction as
// additionalContext), anything else lets the turn end.
func TestTurnEndHandoffOutput(t *testing.T) {
	var calls atomic.Int32
	answer := `{"handoff":true,"reason":"New board messages","context":"Coral board: 2 new messages on team. Run coral-board read to read them, then carry on."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/sessions/live/dev/turn-end" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if body["stop_hook_active"] != false {
			t.Errorf("body %s", b)
		}
		w.Write([]byte(answer))
	}))
	defer srv.Close()

	out := turnEndHandoff(srv.URL, "dev", false)
	var got struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		Hook     struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if got.Decision != "block" || got.Reason != "New board messages" || got.Hook.Event != "Stop" || got.Hook.Context == "" {
		t.Errorf("output %s", out)
	}

	if out := turnEndHandoff(srv.URL, "dev", true); out != nil || calls.Load() != 1 {
		t.Errorf("stop_hook_active: no call, no hand-off (calls %d, out %s)", calls.Load(), out)
	}
	answer = `{"handoff":false}`
	if out := turnEndHandoff(srv.URL, "dev", false); out != nil {
		t.Errorf("no hand-off: %s", out)
	}
	answer = `{"error":"session_token_required"}`
	if out := turnEndHandoff(srv.URL, "dev", false); out != nil {
		t.Errorf("refused: %s", out)
	}
	if out := turnEndHandoff("http://127.0.0.1:1", "dev", false); out != nil {
		t.Errorf("server down: %s", out)
	}
}
