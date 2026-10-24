package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/subgentic/coral/internal/testguard"
)

// TestBoardPathHelperProcess runs coral-board's main with the arguments in
// CORAL_BOARD_HELPER_ARGS. It is a child process of TestEveryBoardCommandEscapesTheBoardName,
// so a command that exits on the fake server's replies doesn't end the test run.
func TestBoardPathHelperProcess(t *testing.T) {
	raw := os.Getenv("CORAL_BOARD_HELPER_ARGS")
	if raw == "" {
		t.Skip("helper process only")
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"coral-board"}, args...)
	main()
	os.Exit(0)
}

// #3640 follow-up: every board command puts the board name in its path escaped. With a board
// named "team?x=1#frag", each command's board requests reach /api/board/team?x=1#frag[/...],
// never /api/board/team with "x=1" as a query. (TestBoardNamesInPathsAreEscaped is the lint that
// keeps new paths escaped; this runs the commands.)
func TestEveryBoardCommandEscapesTheBoardName(t *testing.T) {
	isolateBoardEnv(t)
	const board = "team?x=1#frag"

	var mu sync.Mutex
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/board/") {
			mu.Lock()
			paths = append(paths, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	t.Setenv("CORAL_URL", ts.URL)
	saveState(&boardState{Project: board, JobTitle: "Dev"})

	for _, args := range [][]string{
		{"post", "hello"},
		{"read"},
		{"read", "--all"},
		{"read", "--last", "5"},
		{"read", "--id", "3"},
		{"peek", "Lead"},
		{"check"},
		{"subscribers"},
		{"task", "add", "a task"},
		{"task", "list"},
		{"task", "claim", "1"},
		{"task", "current"},
		{"task", "detail", "1"},
		{"task", "complete", "1"},
		{"task", "cancel", "1", "--message", "r"},
		{"task", "reassign", "1", "--to", "QA"},
		{"task", "amend", "1", "--revision", "1", "--reason", "r", "--body", "b"},
		{"task", "submit-review", "1", "--reason", "r"},
		{"task", "unblock", "1", "--all"},
		{"task", "goal", "1", "2"},
		{"task", "list", "--goal", "2"},
		{"goal", "add", "a goal"},
		{"goal", "list"},
		{"goal", "show", "1"},
		{"goal", "update", "1", "--title", "t"},
		{"goal", "close", "1", "--reason", "r"},
		{"leave"},
	} {
		mu.Lock()
		before := len(paths)
		mu.Unlock()
		enc, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^TestBoardPathHelperProcess$")
		// The child dials the fake server above: CORAL_URL is this test's, not the live Coral.
		cmd.Env = append(os.Environ(), "CORAL_BOARD_HELPER_ARGS="+string(enc), testguard.TestURLEnv+"=1")
		cmd.Stdin = strings.NewReader("")
		out, _ := cmd.CombinedOutput()
		mu.Lock()
		got := append([]string(nil), paths[before:]...)
		mu.Unlock()
		if len(got) == 0 {
			t.Errorf("%v made no board request: %s", args, out)
			continue
		}
		for _, p := range got {
			path := p[strings.Index(p, " ")+1:]
			if path != "/api/board/"+board && !strings.HasPrefix(path, "/api/board/"+board+"/") {
				t.Errorf("%v: %s is not on the board %q", args, p, board)
			}
		}
	}
}
