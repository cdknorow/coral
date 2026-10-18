package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// #3635 (A): CORAL_SESSION_NAME wins over the tmux session name; the tmux lookup is kept (this
// release) for an agent launched before the change, which has TMUX and no usable variable. The lookup runs
// the CORAL_TMUX_BIN binary as "display-message -p #{session_name}"; the test stands in for running it
// (#4805: a real process, even a shell script, outlasted the lookup's 3 s bound under a loaded go test ./...).
func TestResolveSessionID_PrefersCoralSessionNameOverTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in tmux binary is a shell script (tmux.BinaryFromEnv needs an executable file)")
	}
	fake := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORAL_TMUX_BIN", fake)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket,1,0")
	var ran [][]string
	answer, fail := "claude-bbbbbbbb-0000-0000-0000-000000000000\n", error(nil)
	old := runTmux
	runTmux = func(bin string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{bin}, args...))
		return []byte(answer), fail
	}
	t.Cleanup(func() { runTmux = old })

	t.Setenv("CORAL_SESSION_NAME", "claude-aaaaaaaa-0000-0000-0000-000000000000")
	if got := ResolveSessionID("payload"); got != "aaaaaaaa-0000-0000-0000-000000000000" {
		t.Fatalf("CORAL_SESSION_NAME must win over tmux: got %q", got)
	}
	if len(ran) != 0 {
		t.Fatalf("tmux asked although CORAL_SESSION_NAME answered: %q", ran)
	}
	t.Setenv("CORAL_SESSION_NAME", "")
	if got := ResolveSessionID("payload"); got != "bbbbbbbb-0000-0000-0000-000000000000" {
		t.Fatalf("an older agent with only TMUX still resolves through tmux: got %q", got)
	}
	if want := []string{fake, "display-message", "-p", "#{session_name}"}; len(ran) != 1 || strings.Join(ran[0], " ") != strings.Join(want, " ") {
		t.Fatalf("tmux run as %q, want %q", ran, want)
	}
	// A lookup that fails (or times out), or names no Coral session, leaves the payload's id.
	answer, fail = "claude-cccccccc-0000-0000-0000-000000000000\n", errors.New("signal: killed") // what it printed is not used
	if got := ResolveSessionID("payload"); got != "payload" {
		t.Fatalf("a failed tmux lookup: got %q", got)
	}
	answer, fail = "main\n", nil
	if got := ResolveSessionID("payload"); got != "payload" {
		t.Fatalf("a tmux session that isn't Coral's: got %q", got)
	}
}
