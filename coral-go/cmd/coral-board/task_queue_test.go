package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestTaskMarkLandedSendsDigestsAndCommits(t *testing.T) {
	isolateBoardEnv(t)
	t.Setenv("CORAL_SUBSCRIBER_ID", "lead")
	saveState(&boardState{Project: "mq", JobTitle: "Orchestrator"})
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
	sha := "1111111111111111111111111111111111111111"
	os.Args = []string{"coral-board", "task", "mark-landed", "7", "--digest", "d1", "--digest", "d2", "--commit", sha + ":the fix: a colon in a subject", "--commit", sha[:39] + "2"}
	cmdTask()
	if path != "/api/board/mq/tasks/7/mark-landed" {
		t.Fatalf("unexpected endpoint: %s", path)
	}
	if body["subscriber_id"] != "lead" {
		t.Fatalf("missing identity: %#v", body)
	}
	if d := body["digests"].([]any); len(d) != 2 || d[0] != "d1" || d[1] != "d2" {
		t.Fatalf("digests: %#v", body["digests"])
	}
	cs := body["commits"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["sha"] != sha || cs[0].(map[string]any)["subject"] != "the fix: a colon in a subject" || cs[1].(map[string]any)["subject"] != "" {
		t.Fatalf("commits: %#v", cs)
	}
}

func TestQueueReadsTheQueueRoute(t *testing.T) {
	isolateBoardEnv(t)
	saveState(&boardState{Project: "mq", JobTitle: "Orchestrator"})
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"queue":[{"task_id":7,"title":"Fix","author":"worker","approvers":["rev"],"approved_at":"2026-10-21T10:00:00Z","patches":[{"name":"fix","digest":"d2","revision":"base1"}]}]}`))
	}))
	defer server.Close()
	serverURL = server.URL
	before := os.Args
	defer func() { os.Args = before }()
	os.Args = []string{"coral-board", "queue"}
	cmdQueue()
	if method != "GET" || path != "/api/board/mq/queue" {
		t.Fatalf("unexpected request: %s %s", method, path)
	}
}

// A title or a name is written by someone else: the plain printout drops the characters that drive a terminal (ESC and the CSI sequences it
// starts, C1, a direction override); --json prints the data as it is.
func TestQueuePrintoutDropsTerminalControlCharacters(t *testing.T) {
	if got := printable("a\x1b[31mred\x1b[0m‮b​c\x07d\u009be"); got != "a[31mred[0mbcde" {
		t.Fatalf("printable: %q", got)
	}
	isolateBoardEnv(t)
	saveState(&boardState{Project: "mq", JobTitle: "Orchestrator"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"queue":[{"task_id":7,"title":"Fix\u001b[2Jeverything","author":"wor\u001bker","approvers":["rev"],"approved_at":"2026-10-21T10:00:00Z","patches":[{"name":"fix\u001b]0;x\u0007","digest":"d2","revision":"base1"}]}]}`))
	}))
	defer server.Close()
	serverURL = server.URL
	before, stdout := os.Args, os.Stdout
	r, wr, _ := os.Pipe()
	os.Stdout = wr
	os.Args = []string{"coral-board", "queue"}
	cmdQueue()
	wr.Close()
	os.Stdout, os.Args = stdout, before
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 7) {
		t.Fatalf("the printout still carries a control character: %q", out)
	}
	if !strings.Contains(out, "#7  Fix") || !strings.Contains(out, "everything") {
		t.Fatalf("the title is missing: %q", out)
	}
}
