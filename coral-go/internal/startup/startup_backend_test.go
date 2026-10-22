package startup

import "testing"

// #3634: --backend wins; then the saved setting (exactly tmux or pty); then tmux; Windows is pty.
func TestResolveBackend(t *testing.T) {
	for _, c := range []struct {
		flag, saved, goos, want string
		forced                  bool
	}{
		{"", "", "darwin", "pty", false},
		{"", "", "linux", "pty", false},
		{"", "", "windows", "pty", false},
		{"", "pty", "darwin", "pty", false},
		{"", "tmux", "linux", "tmux", false},
		{"", "tmux", "windows", "pty", false},
		{"", "PTY", "darwin", "pty", false},
		{"", "screen", "darwin", "pty", false},
		{"pty", "tmux", "darwin", "pty", true},
		{"tmux", "pty", "linux", "tmux", true},
		{"tmux", "", "windows", "tmux", true},
	} {
		got, forced := ResolveBackend(c.flag, c.saved, c.goos)
		if got != c.want || forced != c.forced {
			t.Errorf("ResolveBackend(%q, %q, %q) = %q, %v; want %q, %v", c.flag, c.saved, c.goos, got, forced, c.want, c.forced)
		}
	}
}
