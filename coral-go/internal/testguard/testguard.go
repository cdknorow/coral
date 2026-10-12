// Package testguard keeps test binaries away from the user's real Coral data
// directory.
//
// The production install lives at ~/.coral: its sessions.db, messageboard.db
// and tmux.sock belong to agents that are running right now. A test that
// resolves a path there (by building a tmux client or config with no data
// dir) can kill live sessions or corrupt their state. CheckDataPath panics
// in that case so the test fails at the call site instead.
package testguard

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// protectedDirs are the real ~/.coral directories, resolved from the passwd
// entry and from $HOME as it was on the first check. A test that later points
// HOME at a temp dir resolves ~/.coral there, which is allowed. They are
// computed only in test binaries: user.Current can block on directory-service
// Macs, and shipped binaries such as the agent hooks never need it.
var protectedDirs = sync.OnceValue(func() []string {
	var dirs []string
	add := func(home string) {
		if home == "" {
			return
		}
		d := filepath.Clean(filepath.Join(home, ".coral"))
		for _, existing := range dirs {
			if existing == d {
				return
			}
		}
		dirs = append(dirs, d)
	}
	if u, err := user.Current(); err == nil {
		add(u.HomeDir)
	}
	add(os.Getenv("HOME"))
	return dirs
})

// enabled reports whether the guard applies. It is a variable so the guard's
// own tests can exercise the non-test path.
var enabled = testing.Testing

// CheckDataPath panics if this is a test binary and path is, or is inside,
// the user's real ~/.coral directory. Outside tests it does nothing.
func CheckDataPath(path string) {
	if path == "" || !enabled() {
		return
	}
	if dir, ok := protected(path); ok {
		panic(fmt.Sprintf("testguard: test resolved %q inside the real Coral data directory %s; "+
			"pass a t.TempDir() (or set CORAL_DATA_DIR / CORAL_TMUX_SOCKET) instead", path, dir))
	}
}

func protected(path string) (string, bool) {
	return protectedIn(path, protectedDirs())
}

// protectedIn reports whether path is, or is inside, one of dirs. Both sides
// are compared as real paths (symlinks resolved), and without regard to case
// on macOS, whose default filesystem is case-insensitive, so ~/.Coral or a
// symlink to ~/.coral is caught too.
func protectedIn(path string, dirs []string) (string, bool) {
	p := comparable(path)
	for _, d := range dirs {
		cd := comparable(d)
		if p == cd || strings.HasPrefix(p, cd+string(filepath.Separator)) {
			return d, true
		}
	}
	return "", false
}

func comparable(path string) string {
	p := realPath(path)
	if runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

// realPath is path made absolute with symlinks resolved. A path that does
// not exist yet (a socket or database about to be created) is resolved
// through its nearest existing ancestor.
func realPath(path string) string {
	p := filepath.Clean(path)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	var rest []string
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}
