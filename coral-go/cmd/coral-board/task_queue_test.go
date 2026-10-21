package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskAttachAndSendBackUseTheirEndpoints(t *testing.T) {
	for _, action := range []string{"attach", "send-back"} {
		t.Run(action, func(t *testing.T) {
			dir := isolateBoardEnv(t)
			t.Setenv("CORAL_SUBSCRIBER_ID", "someone")
			saveState(&boardState{Project: "mq", JobTitle: "Developer"})
			manifest := filepath.Join(dir, "patch.json")
			if err := os.WriteFile(manifest, []byte(`[{"name":"fix","kind":"patch","uri":"coral://artifacts/abc","digest":"abc","revision":"base1"}]`), 0600); err != nil {
				t.Fatal(err)
			}
			var path string
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"task":{"id":7}}`))
			}))
			defer server.Close()
			serverURL = server.URL
			before := os.Args
			defer func() { os.Args = before }()
			if action == "attach" {
				os.Args = []string{"coral-board", "task", "attach", "7", "--artifacts", manifest}
			} else {
				os.Args = []string{"coral-board", "task", "send-back", "7", "--note", "conflict in a.go"}
			}
			cmdTask()
			if path != "/api/board/mq/tasks/7/"+action {
				t.Fatalf("unexpected endpoint: %s", path)
			}
			if body["subscriber_id"] != "someone" {
				t.Fatalf("missing identity: %#v", body)
			}
			if action == "attach" {
				rows := body["artifacts"].([]any)
				if rows[0].(map[string]any)["digest"] != "abc" || rows[0].(map[string]any)["revision"] != "base1" {
					t.Fatalf("manifest changed: %#v", rows)
				}
			} else if body["note"] != "conflict in a.go" {
				t.Fatalf("note lost: %#v", body)
			}
		})
	}
}
