package tmux

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Seams for tests; production code never reassigns them.
var (
	goos          = runtime.GOOS
	executableFn  = os.Executable
	evalSymlinkFn = filepath.EvalSymlinks
)

// bundledTmux returns the tmux that ships next to the running executable (the
// macOS app bundle's Contents/MacOS/tmux) and its terminfo directory
// (Contents/Resources/terminfo), or "" when there is none. Only Darwin looks:
// other platforms rely on the system tmux. The executable path is resolved
// through symlinks first, so a /usr/local/bin/coral link into the bundle still
// finds the bundle.
func bundledTmux() (bin, terminfoDir string) {
	if goos != "darwin" {
		return "", ""
	}
	exe, err := executableFn()
	if err != nil || exe == "" {
		return "", ""
	}
	if resolved, err := evalSymlinkFn(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	candidate := filepath.Join(dir, "tmux")
	if !executableFileExists(candidate) {
		return "", ""
	}
	// A symlinked bundled tmux resolves to the real file too.
	if resolved, err := evalSymlinkFn(candidate); err == nil {
		candidate = resolved
	}
	if ti := filepath.Join(dir, "..", "Resources", "terminfo"); dirExists(ti) {
		terminfoDir = filepath.Clean(ti)
	}
	return candidate, terminfoDir
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// isBundledBinary reports whether bin is the bundled tmux.
func isBundledBinary(bin string) (terminfoDir string, ok bool) {
	b, ti := bundledTmux()
	if b == "" || bin == "" {
		return "", false
	}
	if sameFile(b, bin) {
		return ti, true
	}
	return "", false
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

// tmuxEnv returns the environment for running bin, or nil to inherit ours
// unchanged. Only the bundled tmux needs anything: its terminfo directory is
// appended to TERMINFO_DIRS (after any directories the user already set, so a
// user's TERMINFO / TERMINFO_DIRS keep priority) and CORAL_TMUX_BIN records the
// binary so helpers running inside the panes of a server this client starts can
// call the same tmux. Nothing is changed for a system tmux.
func tmuxEnv(bin string) []string {
	terminfoDir, bundled := isBundledBinary(bin)
	if !bundled {
		return nil
	}
	env := os.Environ()
	if terminfoDir != "" {
		existing := os.Getenv("TERMINFO_DIRS")
		merged := terminfoDir
		if existing != "" {
			if containsPathEntry(existing, terminfoDir) {
				merged = existing
			} else {
				merged = existing + ":" + terminfoDir
			}
		}
		env = setEnv(env, "TERMINFO_DIRS", merged)
	}
	if os.Getenv("CORAL_TMUX_BIN") == "" {
		env = setEnv(env, "CORAL_TMUX_BIN", bin)
	}
	return env
}

func containsPathEntry(list, entry string) bool {
	for _, p := range strings.Split(list, ":") {
		if p == entry {
			return true
		}
	}
	return false
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// BinaryFromEnv is the tmux command that helpers running inside an agent pane
// should use. It is a bounded, cheap lookup (no shell is started): the
// CORAL_TMUX_BIN the server exported, else the tmux bundled next to this
// executable (so a helper in the app bundle still finds its tmux when it talks
// to a server that was started before CORAL_TMUX_BIN was injected), else tmux on
// PATH or in a common install location, else plain "tmux".
func BinaryFromEnv() string {
	if p := strings.TrimSpace(os.Getenv("CORAL_TMUX_BIN")); p != "" {
		if filepath.IsAbs(p) && executableFileExists(p) {
			return p
		}
		if resolved, err := exec.LookPath(p); err == nil {
			return resolved
		}
	}
	if p, _ := bundledTmux(); p != "" {
		return p
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, p := range commonTmuxPaths {
		if executableFileExists(p) {
			return p
		}
	}
	return "tmux"
}

// ── Server compatibility ─────────────────────────────────────────────────

var (
	incompatMu sync.Mutex
	incompat   = map[string]string{} // socket path -> guidance, while the problem persists
)

// IncompatibleServer reports an active client/server version problem on any
// socket this process talks to, with the guidance to show. The state is per
// socket and is cleared as soon as a tmux command on that socket succeeds, so
// it does not go stale after the user recovers.
func IncompatibleServer() (message string, ok bool) {
	incompatMu.Lock()
	defer incompatMu.Unlock()
	for _, m := range incompat {
		return m, true
	}
	return "", false
}

// versionProblem phrases tmux uses when the running server and this client
// cannot work together: "protocol version mismatch" (older/same-protocol
// generations) and "server version is too old for client" (newer tmux).
var versionProblem = [][]byte{
	[]byte("protocol version mismatch"),
	[]byte("server version is too old for client"),
}

// noteTmuxResult updates the compatibility state after a tmux command on
// socketPath. A failure with a version-problem message records guidance; any
// success on that socket clears it. Coral never kills the old server.
func noteTmuxResult(err error, bin, socketPath string) {
	if err == nil {
		incompatMu.Lock()
		delete(incompat, socketPath)
		incompatMu.Unlock()
		return
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return
	}
	stderr := bytes.ToLower(exitErr.Stderr)
	matched := false
	for _, phrase := range versionProblem {
		if bytes.Contains(stderr, phrase) {
			matched = true
		}
	}
	if !matched {
		return
	}
	msg := "The tmux server on this Coral instance's socket (" + socketPath + ") was started by a different tmux version than " + bin +
		", so Coral cannot talk to it. Coral will not stop it, and your agents keep running. Restarting Coral alone does not " +
		"restart that server. Either set CORAL_TMUX_BIN to the tmux that started it, or, when you can end the agents running in it, " +
		"stop it with that tmux (tmux -S <socket> kill-server) and then restart Coral."
	incompatMu.Lock()
	prev := incompat[socketPath]
	incompat[socketPath] = msg
	incompatMu.Unlock()
	if prev != msg {
		log.Printf("[tmux] client/server version problem (socket=%q): %s", socketPath, msg)
	}
}
