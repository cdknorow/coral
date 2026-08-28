package tmux

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The merge of tmux's default socket is what let one Coral instance enumerate
// — and through the session endpoints kill — another instance's agents. It
// must be off for any data directory that is not the default install.
func TestDefaultSocketFallbackIsOffForNonDefaultDataDirectories(t *testing.T) {
	t.Setenv("CORAL_TMUX_NO_FALLBACK", "")
	t.Setenv("CORAL_TMUX_FALLBACK", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	defaultPath := filepath.Join(home, ".coral", "tmux.sock")

	if !defaultSocketFallback(defaultPath) {
		t.Error("the default install must keep the merge, or an upgrade loses sight of running agents")
	}
	for _, other := range []string{
		"/tmp/coral-t1/tmux.sock",
		filepath.Join(home, "coral-test", "tmux.sock"),
		filepath.Join(home, ".coral-two", "tmux.sock"),
	} {
		if defaultSocketFallback(other) {
			t.Errorf("data dir %q must not merge the default socket", other)
		}
	}
}

func TestFallbackEnvironmentOverridesBothWays(t *testing.T) {
	home, _ := os.UserHomeDir()
	defaultPath := filepath.Join(home, ".coral", "tmux.sock")
	custom := "/tmp/coral-t1/tmux.sock"

	t.Run("no-fallback wins over a default install", func(t *testing.T) {
		t.Setenv("CORAL_TMUX_NO_FALLBACK", "1")
		if defaultSocketFallback(defaultPath) {
			t.Error("CORAL_TMUX_NO_FALLBACK=1 must disable the merge")
		}
	})
	t.Run("no-fallback wins over an explicit fallback", func(t *testing.T) {
		t.Setenv("CORAL_TMUX_NO_FALLBACK", "1")
		t.Setenv("CORAL_TMUX_FALLBACK", "1")
		if defaultSocketFallback(custom) {
			t.Error("CORAL_TMUX_NO_FALLBACK must take precedence")
		}
	})
	t.Run("fallback can be forced on for a custom dir", func(t *testing.T) {
		t.Setenv("CORAL_TMUX_NO_FALLBACK", "")
		t.Setenv("CORAL_TMUX_FALLBACK", "1")
		if !defaultSocketFallback(custom) {
			t.Error("CORAL_TMUX_FALLBACK=1 must restore the old behaviour")
		}
	})
	t.Run("only truthy values count", func(t *testing.T) {
		t.Setenv("CORAL_TMUX_FALLBACK", "")
		for _, v := range []string{"0", "false", "no", "", "  "} {
			t.Setenv("CORAL_TMUX_NO_FALLBACK", v)
			if !defaultSocketFallback(defaultPath) {
				t.Errorf("CORAL_TMUX_NO_FALLBACK=%q should not disable the merge", v)
			}
		}
	})
}

// A socket path over the platform limit cannot be bound, and tmux reports it
// as a bare "File name too long" long after startup has claimed the tmux
// backend. Catch it up front instead.
func TestCheckSocketPathRejectsAnOverLimitPath(t *testing.T) {
	if err := CheckSocketPath("/tmp/coral-t1"); err != nil {
		t.Errorf("a short data dir should be usable: %v", err)
	}

	limit := maxUnixSocketPath()
	long := "/tmp/" + strings.Repeat("a", limit) + "/deeper"
	err := CheckSocketPath(long)
	if err == nil {
		t.Fatalf("a %d-byte data dir should be rejected on a %d-byte limit", len(long), limit)
	}
	// The message has to name the limit and the path, or the operator cannot act on it.
	for _, want := range []string{"limit", "tmux.sock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPlatformSocketLimit(t *testing.T) {
	got := maxUnixSocketPath()
	want := 104
	if runtime.GOOS == "linux" {
		want = 108
	}
	if got != want {
		t.Errorf("sun_path limit on %s = %d, want %d", runtime.GOOS, got, want)
	}
}

// The scratchpad path this team was told to use is over the limit; that is how
// the silent downgrade was found. Keep a regression on the shape of it.
func TestARealisticDeepScratchpadPathIsRejected(t *testing.T) {
	deep := "/private/tmp/claude-501/-Users-someone-Software-coral/a48e1332-444d-073b-9a4c-a917b170a5d2/scratchpad/coral-test"
	if len(deep) < maxUnixSocketPath() {
		t.Skipf("sample path is only %d bytes on this platform", len(deep))
	}
	if err := CheckSocketPath(deep); err == nil {
		t.Error("a deep scratchpad data dir should be rejected, not silently downgraded")
	}
}
