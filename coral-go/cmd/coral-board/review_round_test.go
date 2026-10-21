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

// #3805 (Security's SHOULD on #3787): approve and reject send the round their caller last saw
// (task detail, or a claimed review task's own round) when --round isn't given, so a verdict
// typed after a resubmission is refused by the server rather than applied to a revision the
// reviewer hasn't read; an explicit --round wins.
func TestVerdictSendsTheRoundLastSeen(t *testing.T) {
	isolateBoardEnv(t)
	t.Setenv("CORAL_SUBSCRIBER_ID", "QA Engineer")
	saveState(&boardState{Project: "team", JobTitle: "QA Engineer"})
	current := 1 // the server's open round for task 7
	var sent []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/board/team/tasks/7":
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "workflow": map[string]any{"review_rounds": []any{map[string]any{"round": current}}}})
		case r.Method == "GET" && r.URL.Path == "/api/board/team/tasks/9": // the reviewer's review task
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "workflow": map[string]any{"review_of": 7, "review_round": current}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/approve"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body["round"])
			if int(body["round"].(float64)) != current {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":"was resubmitted"}`))
				return
			}
			w.Write([]byte(`{"completed":false}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	run := func(args ...string) {
		before := os.Args
		defer func() { os.Args = before }()
		os.Args = append([]string{"coral-board", "task"}, args...)
		cmdTask()
	}

	run("detail", "7") // the reviewer reads round 1
	current = 2        // the author resubmits: round 2
	if got := lastSeenRound("team", 7); got != 1 {
		t.Fatalf("recorded round = %d, want 1", got)
	}
	exited := captureExit(t, func() { run("approve", "7") })
	if !exited || len(sent) != 1 || sent[0] != float64(1) {
		t.Fatalf("an approval without --round sends the round read (1) and is refused: exited=%v sent=%v", exited, sent)
	}

	run("detail", "9") // reading the review task (round 2 now) records it for #7 too
	if got := lastSeenRound("team", 7); got != 2 {
		t.Fatalf("recorded round after reading the review task = %d, want 2", got)
	}
	run("approve", "7")
	if len(sent) != 2 || sent[1] != float64(2) {
		t.Fatalf("the round read again (2) is sent: %v", sent)
	}
	current = 3
	run("approve", "7", "--round", "3")
	if len(sent) != 3 || sent[2] != float64(3) {
		t.Fatalf("an explicit --round wins: %v", sent)
	}
}

// captureExit runs f and reports whether it called exitFn.
func captureExit(t *testing.T, f func()) (exited bool) {
	t.Helper()
	prev := exitFn
	exitFn = func(int) { panic(exitCalled{}) }
	defer func() {
		exitFn = prev
		if r := recover(); r != nil {
			if _, ok := r.(exitCalled); !ok {
				panic(r)
			}
			exited = true
		}
	}()
	f()
	return false
}

type exitCalled struct{}

// Security's NITs on #3805: the round file's name is a hash, never the session name itself (one
// with a path in it stays in the data folder), and it is written whole.
func TestSeenRoundsFileNameIsAHash(t *testing.T) {
	dir := isolateBoardEnv(t)
	t.Setenv("CORAL_SESSION_NAME", "../../escape/x")
	noteSeenRound("team", map[string]any{"id": float64(5), "workflow": map[string]any{"review_rounds": []any{map[string]any{"round": float64(3)}}}})
	p := seenRoundsPath()
	if filepath.Dir(p) != filepath.Clean(dir) || strings.Contains(filepath.Base(p), "escape") {
		t.Fatalf("round file at %s, want a hashed name in %s", p, dir)
	}
	if lastSeenRound("team", 5) != 3 {
		t.Fatal("the round was not recorded")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".board_review_rounds_") {
			t.Fatalf("a temp file was left: %s", e.Name())
		}
	}
}

// #5689: an approval also sends the revision of the round last read (patches that joined it), so a join after the reading is refused by
// the server; --revision overrides it.
func TestApproveSendsTheRevisionLastSeen(t *testing.T) {
	isolateBoardEnv(t)
	t.Setenv("CORAL_SUBSCRIBER_ID", "QA Engineer")
	saveState(&boardState{Project: "team", JobTitle: "QA Engineer"})
	var sent []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/board/team/tasks/7":
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "workflow": map[string]any{"review_rounds": []any{map[string]any{"round": 2, "revision": 3}}}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/approve"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body["revision"])
			w.Write([]byte(`{"completed":false}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	run := func(args ...string) {
		before := os.Args
		defer func() { os.Args = before }()
		os.Args = append([]string{"coral-board", "task"}, args...)
		cmdTask()
	}
	run("approve", "7") // never read: revision 0
	run("detail", "7")
	if got := lastSeenRevision("team", 7); got != 3 {
		t.Fatalf("recorded revision = %d, want 3", got)
	}
	run("approve", "7")
	run("approve", "7", "--revision", "5")
	if len(sent) != 3 || sent[0] != float64(0) || sent[1] != float64(3) || sent[2] != float64(5) {
		t.Fatalf("revisions sent: %v, want 0 (unread), 3 (read), 5 (explicit)", sent)
	}
}
