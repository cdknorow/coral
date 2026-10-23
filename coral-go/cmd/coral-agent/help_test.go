package main

import (
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/subgentic/coral/internal/hooks"
	"github.com/subgentic/coral/internal/testguard"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// TestHelperRunAgent runs main() with the arguments in CORAL_AGENT_ARGS when
// started as a subprocess by runAgent; otherwise it does nothing.
func TestHelperRunAgent(t *testing.T) {
	if os.Getenv("CORAL_AGENT_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CORAL_AGENT_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	os.Args = append([]string{"coral-agent"}, args...)
	hooks.AllowTestServerListener = true // the test binary serves on the port this CLI talks to (#224 B)
	main()
	os.Exit(0)
}

// runAgent runs coral-agent in a subprocess with no Coral or tmux variables
// from the calling agent, pointed at serverURL.
func runAgent(t *testing.T, serverURL string, args ...string) (int, string, string) {
	t.Helper()
	return runAgentEnv(t, serverURL, nil, args...)
}

// runAgentEnv is runAgent with more environment variables (kept after the cleaned ones).
func runAgentEnv(t *testing.T, serverURL string, extraEnv []string, args ...string) (int, string, string) {
	t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunAgent$")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CORAL_") && !strings.HasPrefix(kv, "TMUX") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	dir := t.TempDir()
	cmd.Env = append(cmd.Env, "CORAL_AGENT_HELPER=1", "CORAL_AGENT_ARGS="+string(encoded),
		"CORAL_URL="+serverURL, testguard.TestURLEnv+"=1", "CORAL_DATA_DIR="+dir, "CORAL_DIR="+dir, "HOME="+t.TempDir(),
		"CORAL_SESSION_NAME=claude-00000000-0000-4000-8000-000000000000")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, stdout.String(), stderr.String()
}

func failingServer(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.String()+" "+string(body))
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reqs...)
	}
}

func TestAgentHelpFlagsPrintUsageWithoutSideEffects(t *testing.T) {
	srv, requests := failingServer(t)
	cases := [][]string{{"--help"}, {"-h"}, {"help"}, {"task", "help"}, {"ui", "help"}, {"history", "help"}}
	for cmd := range commands {
		cases = append(cases, []string{cmd, "--help"}, []string{cmd, "-h"})
	}
	for _, sub := range []string{"add", "list", "claim", "current", "detail", "edit", "publish", "complete", "cancel"} {
		cases = append(cases, []string{"task", sub, "--help"}, []string{"task", sub, "-h"})
	}
	cases = append(cases,
		[]string{"search", "deploy", "--help"},
		[]string{"history", "search", "-h"},
		[]string{"history", "context", "--help"},
		[]string{"ui", "publish", "--help"},
		[]string{"artifact", "upload", "--help"},
		[]string{"artifact", "download", "-h"},
		[]string{"launch", ".", "--prompt", "hi", "-h"},
		[]string{"bind-codex", "--help", "x"},
		[]string{"task", "complete", "3", "--message", "done", "--help"},
	)
	// The cases run in parallel inside one group, which returns only when all
	// of them have finished, so the request check below sees every one.
	t.Run("cases", func(t *testing.T) {
		for _, args := range cases {
			t.Run(strings.Join(args, "_"), func(t *testing.T) {
				t.Parallel()
				code, stdout, stderr := runAgent(t, srv.URL, args...)
				if code != 0 {
					t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
				}
				if !strings.Contains(stdout, "coral-agent") {
					t.Errorf("no usage on stdout: %q", stdout)
				}
				if len(args) > 1 && args[0] == "artifact" && args[1] == "upload" {
					for _, guidance := range []string{"Upload long reports, logs, diffs and evidence", "coral://artifacts/<digest>", "one-line", "numbered points, one per line", "coral-board post - <<'EOF'"} {
						if !strings.Contains(stdout, guidance) {
							t.Errorf("artifact upload help missing %q", guidance)
						}
					}
				}
			})
		}
	})
	if reqs := requests(); len(reqs) != 0 {
		t.Fatalf("help made %d request(s): %v", len(reqs), reqs)
	}
}

func TestAgentFlagShapedTextIsRejected(t *testing.T) {
	srv, requests := failingServer(t)
	for _, args := range [][]string{
		{"task", "complete", "3", "--message", "--bogus"},
		{"task", "cancel", "3", "--message=-x"},
		{"task", "add", "Title", "--body", "--priority"},
		{"launch", ".", "--prompt", "--model"},
	} {
		if code, _, stderr := runAgent(t, srv.URL, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, stderr)
		}
	}
	if reqs := requests(); len(reqs) != 0 {
		t.Fatalf("rejected input made %d request(s): %v", len(reqs), reqs)
	}
}

// A note that merely mentions --help is still sent.
func TestAgentTextContainingHelpIsSent(t *testing.T) {
	srv, requests := failingServer(t)
	runAgent(t, srv.URL, "task", "complete", "3", "--message", "fixed --help handling")
	reqs := requests()
	if len(reqs) == 0 || !strings.Contains(strings.Join(reqs, "\n"), "fixed --help handling") {
		t.Fatalf("note not sent: %v", reqs)
	}
}

// "--" lets a search query start with a dash: everything after it is query.
func TestAgentSearchQueryAfterDoubleDash(t *testing.T) {
	srv, requests := failingServer(t)
	runAgent(t, srv.URL, "search", "--", "--help", "flag")
	reqs := strings.Join(requests(), "\n")
	if !strings.Contains(reqs, "--help flag") && !strings.Contains(reqs, "--help+flag") && !strings.Contains(reqs, "--help%20flag") {
		t.Fatalf("query not sent: %q", reqs)
	}
}

// Each task page lists its real options, and an artifact page is about its own operation (#282 rows 4, 7, 8).
func TestAgentHelpListsRealOptions(t *testing.T) {
	srv, _ := failingServer(t)
	_, out, _ := runAgent(t, srv.URL, "task", "edit", "--help")
	for _, want := range []string{"--title", "--body", "--priority", "--blocked-by"} {
		assert.Contains(t, out, want)
	}
	_, out, _ = runAgent(t, srv.URL, "task", "complete", "--help")
	for _, want := range []string{"--message", "--outcome", "--artifacts", "--candidate-revision"} {
		assert.Contains(t, out, want)
	}
	_, out, _ = runAgent(t, srv.URL, "task", "list", "--help")
	assert.Contains(t, out, "[--json]")
	assert.Contains(t, out, "your tasks")
	_, out, _ = runAgent(t, srv.URL, "task", "current", "--help")
	assert.Contains(t, out, "[--json]")
	_, out, _ = runAgent(t, srv.URL, "artifact", "download", "--help")
	assert.Contains(t, out, "artifact download")
	assert.NotContains(t, out, "artifact upload")
	assert.NotContains(t, out, "<<'EOF'")
	_, out, _ = runAgent(t, srv.URL, "artifact", "upload", "--help")
	assert.NotContains(t, out, "artifact download")
	assert.Contains(t, out, "Bash")
}
