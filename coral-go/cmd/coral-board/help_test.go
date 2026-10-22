package main

import (
	"encoding/json"
	"errors"
	"github.com/subgentic/coral/internal/testguard"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestHelperRunBoard runs main() with the arguments in CORAL_BOARD_ARGS when
// started as a subprocess by runBoard; otherwise it does nothing.
func TestHelperRunBoard(t *testing.T) {
	if os.Getenv("CORAL_BOARD_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CORAL_BOARD_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	os.Args = append([]string{"coral-board"}, args...)
	main()
	os.Exit(0)
}

type boardRun struct {
	code           int
	stdout, stderr string
}

// runBoard runs coral-board in a subprocess whose environment holds no Coral
// or tmux variables from the calling agent: only serverURL and a temporary
// data dir, so nothing can reach a real server or a real board state file.
func runBoard(t *testing.T, serverURL, dataDir string, args ...string) boardRun {
	t.Helper()
	return runBoardInput(t, serverURL, dataDir, nil, args...)
}

// runBoardInput is runBoard with stdin (nil: the null device, as for runBoard).
func runBoardInput(t *testing.T, serverURL, dataDir string, stdin io.Reader, args ...string) boardRun {
	t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunBoard$")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CORAL_") && !strings.HasPrefix(kv, "TMUX") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env,
		"CORAL_BOARD_HELPER=1", "CORAL_BOARD_ARGS="+string(encoded),
		"CORAL_URL="+serverURL, testguard.TestURLEnv+"=1", "CORAL_DATA_DIR="+dataDir, "CORAL_DIR="+dataDir,
		"HOME="+t.TempDir(), "CORAL_SESSION_NAME=help-test", "CORAL_SUBSCRIBER_ID=Tester")
	var stdout, stderr strings.Builder
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return boardRun{code, stdout.String(), stderr.String()}
}

type recordingServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newRecordingServer(t *testing.T) *recordingServer {
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.reqs = append(rs.reqs, r.Method+" "+r.URL.Path+" "+string(body))
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id": 1}`))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *recordingServer) requests() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.reqs...)
}

// subscribedDataDir returns a data dir whose board state subscribes the
// subprocess to board "team" on serverURL.
func subscribedDataDir(t *testing.T, serverURL string) string {
	dir := t.TempDir()
	state, _ := json.Marshal(boardState{Project: "team", JobTitle: "Tester", ServerURL: serverURL})
	if err := os.WriteFile(filepath.Join(dir, "board_state_help-test.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every command and subcommand answers --help and -h with usage, exit 0, and
// no request: not even state or identity resolution reaches the server.
func TestHelpFlagsPrintUsageWithoutSideEffects(t *testing.T) {
	srv := newRecordingServer(t)
	dataDir := subscribedDataDir(t, srv.URL)

	cases := [][]string{{"--help"}, {"-h"}, {"help"}, {"task", "help"}, {"workflow", "help"}}
	for cmd := range commands {
		cases = append(cases, []string{cmd, "--help"}, []string{cmd, "-h"})
	}
	for _, sub := range []string{"add", "list", "claim", "current", "detail", "submit-review", "release-review", "attach", "send-back", "mark-landed", "complete", "cancel", "reassign", "unblock", "amend"} {
		cases = append(cases, []string{"task", sub, "--help"}, []string{"task", sub, "-h"})
	}
	for _, sub := range []string{"create", "list", "trigger", "status", "runs", "kill"} {
		cases = append(cases, []string{"workflow", sub, "--help"}, []string{"workflow", sub, "-h"})
	}
	cases = append(cases,
		[]string{"post", "hello", "--help"},
		[]string{"post", "--to", "Lead", "-h"},
		[]string{"task", "complete", "12", "--message", "done", "--help"},
		[]string{"wait", "--from", "Lead", "-h"},
		[]string{"join", "team", "--as", "Dev", "--help"},
	)

	// The cases run in parallel inside one group, which returns only when all
	// of them have finished, so the request check below sees every one.
	t.Run("cases", func(t *testing.T) {
		for _, args := range cases {
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				t.Parallel()
				res := runBoard(t, srv.URL, dataDir, args...)
				if res.code != 0 {
					t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", res.code, res.stdout, res.stderr)
				}
				if !strings.Contains(res.stdout, "Usage: coral-board") {
					t.Errorf("no usage on stdout: %q", res.stdout)
				}
			})
		}
	})
	if reqs := srv.requests(); len(reqs) != 0 {
		t.Fatalf("help made %d request(s): %v", len(reqs), reqs)
	}
}

func TestHelpShowsTheCommandsOwnUsage(t *testing.T) {
	srv := newRecordingServer(t)
	dataDir := subscribedDataDir(t, srv.URL)
	for args, want := range map[string]string{
		"post --help":             `post "<message>"`,
		"task complete -h":        "complete <id>",
		"task -h":                 "Workflow options for add",
		"workflow trigger --help": "trigger <name>",
		"wait --help":             "--timeout <dur>",
	} {
		t.Run(args, func(t *testing.T) {
			t.Parallel()
			res := runBoard(t, srv.URL, dataDir, strings.Fields(args)...)
			if !strings.Contains(res.stdout, want) {
				t.Errorf("%s: usage %q does not mention %q", args, res.stdout, want)
			}
			if strings.HasPrefix(args, "post ") {
				for _, guidance := range []string{"one-line summary", "one per line; answer by number", "@Name", "SHAs, branches and paths", "coral-agent artifact upload <file>", "coral://artifacts/<digest>", "Multi-line posts and anything with backticks"} {
					if !strings.Contains(res.stdout, guidance) {
						t.Errorf("%s: usage missing posting guidance %q", args, guidance)
					}
				}
			}
		})
	}
}

func TestPostSendsTextThatOnlyContainsHelp(t *testing.T) {
	srv := newRecordingServer(t)
	dataDir := subscribedDataDir(t, srv.URL)

	res := runBoard(t, srv.URL, dataDir, "post", "see --help for usage")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	res = runBoard(t, srv.URL, dataDir, "post", "--", "--help is a flag")
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || !strings.Contains(reqs[0], `"content":"see --help for usage"`) || !strings.Contains(reqs[1], `"content":"--help is a flag"`) {
		t.Fatalf("requests = %v", reqs)
	}
}

// Free text that is really an option is a usage error (exit 2) and is never
// sent.
func TestFlagShapedTextIsRejected(t *testing.T) {
	srv := newRecordingServer(t)
	dataDir := subscribedDataDir(t, srv.URL)
	for _, args := range [][]string{
		{"post", "--bogus"},
		{"post", "-x", "hello"},
		{"post", "--to", "Lead", "--verbose", "hi"},
		{"task", "complete", "5", "--message", "--bogus"},
		{"task", "complete", "5", "--message=-x"},
		{"task", "cancel", "5", "-message", "--force"},
		{"task", "add", "Title", "--body", "--priority"},
		{"wait", "--from", "Lead", "--reason", "--block"},
	} {
		res := runBoard(t, srv.URL, dataDir, args...)
		if res.code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, res.code, res.stderr)
		}
	}
	if reqs := srv.requests(); len(reqs) != 0 {
		t.Fatalf("rejected input made %d request(s): %v", len(reqs), reqs)
	}
}

func TestCheckFreeTextAllowsOrdinaryText(t *testing.T) {
	for _, args := range [][]string{
		{"post", "see --help for usage"},
		{"post", "-1 is the answer"},
		{"post", "- a bullet"},
		{"post", "-5% latency"},
		{"post", "--- summary ---"},
		{"post", "- [x] done"},
		{"post", "->", "next"},
		{"post", "-5%", "faster"},
		{"task", "complete", "5", "--message", "-5% faster"},
		{"post", "--to", "Lead", "done"},
		{"post", "--", "--literal"},
		{"task", "complete", "5", "--message", "fixed --help handling"},
		{"task", "complete", "5", "--message", "-"},
		{"unknown-command", "--message", "--x"},
	} {
		if err := checkFreeText(args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}
