package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskUnblockSendsExplicitRemoval(t *testing.T) {
	isolateBoardEnv(t)
	t.Setenv("CORAL_SUBSCRIBER_ID", "Orchestrator")
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/board/team/tasks/123" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":123,"status":"pending"}`))
	}))
	defer server.Close()
	serverURL = server.URL
	cmdTaskUnblock(&boardState{Project: "team"}, []string{"123", "--blocker", "99"})
	if payload["remove_blocker"] != float64(99) || payload["subscriber_id"] != "Orchestrator" {
		t.Fatal(payload)
	}
	payload = nil
	cmdTaskUnblock(&boardState{Project: "team"}, []string{"123", "--all"})
	if payload["clear_blockers"] != true || payload["remove_blocker"] != nil {
		t.Fatal(payload)
	}
}
