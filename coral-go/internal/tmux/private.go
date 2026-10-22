package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Coral's agents left in its own tmux server after Coral switched to the pty backend (#3634).
// A Coral on pty can't reach them, and they keep running (and spending tokens) until ended.
// These helpers act on exactly one tmux server, the one on Coral's private socket
// (<data dir>/tmux.sock), never tmux's default socket or the operator's own tmux, and only on
// sessions named like Coral's agents ({type}-{uuid}).

var agentSessionRE = regexp.MustCompile(`(?i)^\w+-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// PrivateSocketPath is Coral's tmux socket in the data folder coralDir.
func PrivateSocketPath(coralDir string) string { return filepath.Join(coralDir, "tmux.sock") }

// privateSocketOK: the path itself is a socket, never a symlink (Lstat), so a link planted at
// <data dir>/tmux.sock can't point these helpers at another tmux server (Security, #3634).
func privateSocketOK(socket string) bool {
	st, err := os.Lstat(socket)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

func privateTmux(ctx context.Context, socket string, args ...string) (string, error) {
	if !privateSocketOK(socket) {
		return "", os.ErrNotExist
	}
	bin, ok := IsAvailable()
	if !ok {
		return "", exec.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"-S", socket}, args...)...)
	// Never the tmux this process may run inside: only the -S socket.
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if name, _, _ := strings.Cut(e, "="); !strings.EqualFold(name, "TMUX") && !strings.EqualFold(name, "TMUX_PANE") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.Output()
	return string(out), err
}

// PrivateAgentSessions lists the Coral agent sessions still running on the tmux server at
// socket. No socket, no server or no tmux: none.
func PrivateAgentSessions(ctx context.Context, socket string) []string {
	if !privateSocketOK(socket) {
		return nil
	}
	out, err := privateTmux(ctx, socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); agentSessionRE.MatchString(name) {
			names = append(names, name)
		}
	}
	return names
}

// noServer reports tmux's own words for "there is no server on this socket": the socket is stale or nobody listens on it.
func noServer(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no server running") || strings.Contains(s, "no such file or directory") || strings.Contains(s, "connection refused")
}

// PrivateAgentSessionsChecked is PrivateAgentSessions that says whether it could tell. known is false when a tmux server may be there and could not be asked (a timeout, a client/server version
// mismatch, an unreadable answer): the caller must not take that for "none" (#6064: hiding agents that are still running is the one thing the backend choice must never do). No socket, no server
// and no tmux to ask are known, and none.
func PrivateAgentSessionsChecked(ctx context.Context, socket string) (names []string, known bool) {
	if !privateSocketOK(socket) {
		return nil, true
	}
	out, err := privateTmux(ctx, socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return nil, true // no tmux to ask: its agents cannot be reached by tmux either
		case errors.As(err, &ee) && noServer(string(ee.Stderr)):
			return nil, true
		}
		return nil, false
	}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); agentSessionRE.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, true
}

// HasPrivateAgentSession reports whether the agent session name still runs on the tmux server
// at socket.
func HasPrivateAgentSession(ctx context.Context, socket, name string) bool {
	if !agentSessionRE.MatchString(name) {
		return false
	}
	for _, n := range PrivateAgentSessions(ctx, socket) {
		if n == name {
			return true
		}
	}
	return false
}

// EndPrivateAgentSessions ends every Coral agent session on the tmux server at socket and
// returns the names it ended. Other sessions on that server are left alone; when none are
// left, tmux exits by itself.
func EndPrivateAgentSessions(ctx context.Context, socket string) []string {
	var ended []string
	for _, name := range PrivateAgentSessions(ctx, socket) {
		// "=name" matches the session name exactly (no prefix match).
		if _, err := privateTmux(ctx, socket, "kill-session", "-t", "="+name); err == nil {
			ended = append(ended, name)
		}
	}
	return ended
}
