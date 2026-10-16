package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// #3635 (A): CORAL_SESSION_NAME wins over the tmux session name; the tmux lookup is kept (this
// release) for an agent launched before the change, which has TMUX and no usable variable.
func TestResolveSessionID_PrefersCoralSessionNameOverTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tmux is a shell script")
	}
	fake := filepath.Join(t.TempDir(), "tmux")
	script := "#!/bin/sh\necho claude-bbbbbbbb-0000-0000-0000-000000000000\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORAL_TMUX_BIN", fake)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket,1,0")

	t.Setenv("CORAL_SESSION_NAME", "claude-aaaaaaaa-0000-0000-0000-000000000000")
	if got := ResolveSessionID("payload"); got != "aaaaaaaa-0000-0000-0000-000000000000" {
		t.Fatalf("CORAL_SESSION_NAME must win over tmux: got %q", got)
	}
	t.Setenv("CORAL_SESSION_NAME", "")
	if got := ResolveSessionID("payload"); got != "bbbbbbbb-0000-0000-0000-000000000000" {
		t.Fatalf("an older agent with only TMUX still resolves through tmux: got %q", got)
	}
}
