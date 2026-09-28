package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskReviewCommandsPreserveCandidateAndUseDistinctEndpoints(t *testing.T) {
	for _, action := range []string{"submit-review", "release-review"} {
		t.Run(action, func(t *testing.T) {
			dir := isolateBoardEnv(t)
			t.Setenv("CORAL_SUBSCRIBER_ID", "reviewer")
			saveState(&boardState{Project: "review", JobTitle: "Orchestrator"})
			manifest := filepath.Join(dir, "candidate.json")
			if err := os.WriteFile(manifest, []byte(`[{"name":"build","content":"candidate","revision":"revision-42"}]`), 0600); err != nil {
				t.Fatal(err)
			}
			var path string
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				if r.Method != "POST" {
					t.Errorf("method: %s", r.Method)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":42}`))
			}))
			defer server.Close()
			serverURL = server.URL
			before := os.Args
			defer func() { os.Args = before }()
			os.Args = []string{"coral-board", "task", action, "42", "--reason", "review pending", "--message", "candidate ready", "--artifacts", manifest}
			cmdTask()
			if path != "/api/board/review/tasks/42/"+action {
				t.Fatalf("unexpected endpoint: %s", path)
			}
			if body["subscriber_id"] != "reviewer" || body["reason"] != "review pending" {
				t.Fatalf("missing review identity/reason: %#v", body)
			}
			if action == "submit-review" {
				artifacts := body["artifacts"].([]any)
				if artifacts[0].(map[string]any)["revision"] != "revision-42" {
					t.Fatalf("candidate changed: %#v", artifacts)
				}
			}
		})
	}
}
