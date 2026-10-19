package tmux

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/cdknorow/coral/internal/config"
)

// defaultSocketFallback decides whether session discovery also reads tmux's
// default socket.
//
// The merge exists so that upgrading from a Coral old enough to have used the
// default socket does not lose sight of already-running agents. That only ever
// applies to an install rooted at the default ~/.coral. For any other data
// directory there is no upgrade to serve, and the merge is actively harmful:
// a second Coral instance — its own port, its own database, its own socket —
// still enumerates the first instance's sessions, and the session endpoints
// that can kill an agent take those same names. A user with two instances can
// kill the first one's agents from a dashboard that looks entirely like their
// own.
//
// So the merge is on only for a default-rooted install, and either environment
// variable overrides that decision explicitly.
func defaultSocketFallback(socketPath string) bool {
	if config.Truthy(os.Getenv("CORAL_TMUX_NO_FALLBACK")) {
		return false
	}
	if config.Truthy(os.Getenv("CORAL_TMUX_FALLBACK")) {
		return true
	}
	return socketPath == "" || socketPath == defaultSocketPath()
}

// defaultSocketPath is the socket an install rooted at ~/.coral uses.
func defaultSocketPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".coral", "tmux.sock")
}

// maxUnixSocketPath is the sun_path limit for unix domain sockets: 104 bytes
// on macOS and the BSDs, 108 on Linux. A socket path at or over the limit
// cannot be bound at all.
func maxUnixSocketPath() int {
	if runtime.GOOS == "linux" {
		return 108
	}
	return 104
}

// CheckSocketPath reports whether a tmux socket can actually be created for
// this data directory. An over-limit path fails at bind time with a bare
// "File name too long", which previously left the server logging that it was
// using the tmux backend while nothing tmux-related could work.
func CheckSocketPath(coralDir string) error {
	path := filepath.Join(coralDir, "tmux.sock")
	if limit := maxUnixSocketPath(); len(path) >= limit {
		return fmt.Errorf("tmux socket path is %d bytes, over this platform's %d-byte limit: %s",
			len(path), limit, path)
	}
	return nil
}

// defaultServerMayRun reports whether tmux's default socket may have a server: false only
// when its socket file is not there, so the fallback listing (a tmux run on every ListPanes,
// #3728) is skipped on a computer with no default tmux server. Inside tmux ($TMUX), or when the
// path can't be worked out or checked, it says yes, and the listing runs as before.
func defaultServerMayRun() bool {
	if os.Getenv("TMUX") != "" || runtime.GOOS == "windows" {
		return true
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	_, err := os.Lstat(filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default"))
	return !errors.Is(err, fs.ErrNotExist)
}
