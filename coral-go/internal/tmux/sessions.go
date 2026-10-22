package tmux

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agentprio"

	"github.com/subgentic/coral/internal/agentenv"
)

// KillSession kills the tmux session for a given agent.
func (c *Client) KillSession(ctx context.Context, agentName, agentType, sessionID string) error {
	// The agent's terminal log is kept: it is the user's record of the session, and Coral never
	// deletes user data without asking (#4437; the pty backend keeps it too). The launch temp
	// files (prompt, settings, ...) are removed by the session's teardown in routes
	// (agent.CleanupTempFiles) (#4381).
	return c.killTmuxSession(ctx, agentName, agentType, sessionID)
}

// KillSessionOnly kills a tmux session. Used by sleep, to keep state for a later wake; since #4437
// KillSession keeps the terminal log as well, so the two differ only in name.
func (c *Client) KillSessionOnly(ctx context.Context, agentName, agentType, sessionID string) error {
	return c.killTmuxSession(ctx, agentName, agentType, sessionID)
}

// killTmuxSession finds and kills a tmux session by agent identity.
func (c *Client) killTmuxSession(ctx context.Context, agentName, agentType, sessionID string) error {
	c.forgetCaptureTargets(sessionID)
	pane, err := c.FindPane(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if pane == nil {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	_, err = c.run(ctx, "kill-session", "-t", pane.SessionName)
	if err != nil {
		return c.GoneAfter(ctx, agentName, agentType, sessionID, fmt.Errorf("kill-session failed: %w", err))
	}
	return nil
}

// ResizePane resizes a tmux pane width.
func (c *Client) ResizePane(ctx context.Context, agentName string, columns int, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}

	_, err = c.run(ctx, "resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns))
	return c.GoneAfter(ctx, agentName, agentType, sessionID, err)
}

// HasSession checks if a tmux session with the given name exists.
func (c *Client) HasSession(ctx context.Context, name string) bool {
	_, err := c.run(ctx, "has-session", "-t", name)
	return err == nil
}

// NewSession creates a new detached tmux session.
// It automatically disables bracketed paste mode to prevent '00~' characters
// from being prepended to commands sent via send-keys.
func (c *Client) NewSession(ctx context.Context, name, workDir string) error {
	log.Printf("[tmux] NewSession requested: name=%q work_dir=%q tmux_bin=%q socket=%q", name, workDir, c.TmuxBin, c.SocketPath)
	c.scrubServerEnv(ctx)
	out, err := c.run(ctx, "new-session", "-d", "-P", "-F", "#{pane_pid}", "-s", name, "-c", workDir)
	if err != nil {
		return err
	}
	// The pane and everything started in it run at the agents' lower priority
	// (#3437); the tmux server itself keeps the server's.
	applyAgentPriority(out)
	c.run(ctx, "set-option", "-t", name, "history-limit", "50000")
	return nil
}

// applyAgentPriority lowers the priority of the pane process whose pid tmux
// printed (#3437). A renice in the pane's first moments can be lost while
// tmux's freshly forked child is still setting up (seen on macOS, about 1 in
// 15), so it is applied again shortly after, to the pane and anything it has
// started by then, so a child forked before the renice isn't missed.
func applyAgentPriority(out string) {
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return
	}
	agentprio.Apply(pid)
	scheduleAgentPriorityRecheck(pid)
}

// The second look is a one-shot timer that runs a ps (agentprio.ApplyTree). It is bounded (ps has its own timeout) and nothing is left after it, so it is not a leak in a running Coral,
// but a test that ends in the 150 ms before it fires, or while its ps runs, ended with a live timer goroutine (#5217, found by -shuffle). The pending ones are kept so that drainAgentPriority
// (the test main's, before its leak check) can stop what has not fired and wait for what has.
var (
	priorityMu      sync.Mutex
	priorityPending = map[*time.Timer]struct{}{}
	priorityRunning sync.WaitGroup
)

func scheduleAgentPriorityRecheck(pid int) {
	priorityMu.Lock()
	defer priorityMu.Unlock()
	priorityRunning.Add(1)
	var t *time.Timer
	t = time.AfterFunc(agentPriorityRecheck, func() {
		defer priorityRunning.Done()
		priorityMu.Lock()
		delete(priorityPending, t)
		priorityMu.Unlock()
		agentprio.ApplyTree(pid)
	})
	priorityPending[t] = struct{}{}
}

// drainAgentPriority stops the rechecks that have not fired yet and waits for the ones running.
func drainAgentPriority() {
	priorityMu.Lock()
	for t := range priorityPending {
		if t.Stop() {
			priorityRunning.Done() // it will never run
		}
		delete(priorityPending, t)
	}
	priorityMu.Unlock()
	priorityRunning.Wait()
}

// agentPriorityRecheck is when the pane's priority is applied a second time.
var agentPriorityRecheck = 150 * time.Millisecond

// LowerExistingPanes lowers the priority of every pane already running on this
// client's own socket, and their children, once at startup: a tmux server from
// before #3437 started them at normal priority. Only our socket, never the
// user's default server: the list goes straight to our socket, without run's
// fallback, so when Coral's tmux server isn't running yet (after a reboot) it
// lowers nothing rather than the user's own panes.
func (c *Client) LowerExistingPanes(ctx context.Context) int {
	if c.SocketPath == "" {
		return 0
	}
	out, err := c.runOnSocket(ctx, c.SocketPath, "list-panes", "-a", "-F", "#{pane_pid}")
	if err != nil {
		return 0
	}
	var pids []int
	for _, f := range strings.Fields(out) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return agentprio.ApplyTrees(pids) // one process snapshot for every pane (#4129)
}

// scrubServerEnv keeps Coral's server-only settings out of new agent panes (#3389). A tmux
// server started by Coral already has the agent environment (tmuxEnv), but one that was
// already running (started by an older Coral, or by the user on the same socket) keeps the
// environment it started with, and new panes get its global environment. Each such
// CORAL_ variable is marked to be removed before a new pane starts (set-environment -g -r).
// No server yet: nothing to do, new-session starts one with the filtered environment.
func (c *Client) scrubServerEnv(ctx context.Context) {
	if c.SocketPath != "" && !c.FallbackToDefault {
		if _, err := os.Stat(c.SocketPath); err != nil {
			return // no server on this socket yet (and no log line for asking)
		}
	}
	out, err := c.run(ctx, "show-environment", "-g")
	if err != nil {
		return
	}
	identity := map[string]bool{}
	for _, k := range agentenv.IdentityVars {
		identity[k] = true
	}
	have := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		have[name] = value
		// "-NAME" is already marked for removal; CORAL_TMUX_BIN is set on purpose (tmuxEnv).
		// An agent identity is never the server's (#3421): removed like a server setting.
		if !agentenv.IsCoral(name) || strings.EqualFold(name, "CORAL_TMUX_BIN") || (agentenv.Keep(name) && !identity[strings.ToUpper(name)]) {
			continue
		}
		if _, err := c.run(ctx, "set-environment", "-g", "-r", name); err != nil {
			log.Printf("[tmux] could not keep %s out of new panes: %v", name, err)
		}
	}
	// Where the server and its data are: this Coral's own values (startup set them, #3421),
	// not those of whatever started the tmux server.
	for _, k := range agentenv.LocationVars {
		v, set := os.LookupEnv(k)
		old, had := have[k]
		switch {
		case set && (!had || old != v):
			if _, err := c.run(ctx, "set-environment", "-g", "--", k, v); err != nil { // "--": hygiene, as for send-keys
				log.Printf("[tmux] could not set %s for new panes: %v", k, err)
			}
		case !set && had:
			if _, err := c.run(ctx, "set-environment", "-g", "-r", k); err != nil {
				log.Printf("[tmux] could not keep %s out of new panes: %v", k, err)
			}
		}
	}
}

// SetEnvironment sets an environment variable for a tmux session.
func (c *Client) SetEnvironment(ctx context.Context, session, key, value string) error {
	_, err := c.run(ctx, "set-environment", "-t", session, "--", key, value) // "--": hygiene (tmux stops at the name; #3690)
	return err
}

// PipePane sets up pipe-pane logging for a tmux session.
func (c *Client) PipePane(ctx context.Context, target, logFile string) error {
	_, err := c.run(ctx, "pipe-pane", "-t", target, "-o", pipePaneCommand(logFile))
	return err
}

// pipePaneCommand is the shell command tmux runs for pipe-pane: the log path as one quoted word,
// so an apostrophe in the data folder (/home/o'brien) can't end the quote and run the rest of
// the name (#3930, Security's #3927 finding 4).
func pipePaneCommand(logFile string) string {
	return "cat >> " + shellQuote(logFile)
}

// ClosePipePane closes the existing pipe-pane for a target (kills the cat process).
func (c *Client) ClosePipePane(ctx context.Context, target string) error {
	_, err := c.run(ctx, "pipe-pane", "-t", target)
	return err
}

// RenameSession renames a tmux session.
func (c *Client) RenameSession(ctx context.Context, oldName, newName string) error {
	_, err := c.run(ctx, "rename-session", "-t", oldName, newName)
	return err
}

// RespawnPane kills the running process in a pane and spawns a fresh shell.
func (c *Client) RespawnPane(ctx context.Context, target, workDir string) error {
	args := []string{"respawn-pane", "-k", "-t", target}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return err
	}
	if out, err := c.DisplayMessage(ctx, target, "#{pane_pid}"); err == nil {
		applyAgentPriority(out) // #3437
	}
	return nil
}

// ClearHistory clears the tmux pane scrollback history.
func (c *Client) ClearHistory(ctx context.Context, target string) error {
	_, err := c.run(ctx, "clear-history", "-t", target)
	return err
}

// AttachCommand returns the full tmux attach command string including -S flag
// if a custom socket path is configured. Used for display and osascript.
//
// It names the binary this client actually uses: plain "tmux" for a tmux on
// PATH (unchanged output), otherwise its quoted absolute path, with the
// bundled terminfo directory prefixed when the bundled tmux is selected, so a
// pasted command attaches with the same tmux that created the session.
func (c *Client) AttachCommand(sessionName string) string {
	bin := c.attachBinary()
	prefix := ""
	if terminfoDir, bundled := isBundledBinary(c.resolveTmuxBin()); bundled && terminfoDir != "" {
		// Keep the user's whole list; add the bundle only if it is not there.
		dirs := terminfoDir
		if existing := os.Getenv("TERMINFO_DIRS"); existing != "" {
			dirs = existing
			if !containsPathEntry(existing, terminfoDir) {
				dirs = existing + ":" + terminfoDir
			}
		}
		prefix = "TERMINFO_DIRS=" + quoteIfNeeded(dirs) + " "
	}
	if c.SocketPath != "" {
		return fmt.Sprintf("%s%s -S %s attach -t %s", prefix, bin, quoteIfNeeded(c.SocketPath), quoteIfNeeded(sessionName))
	}
	return fmt.Sprintf("%s%s attach -t %s", prefix, bin, quoteIfNeeded(sessionName))
}

// attachBinary is how the attach command names tmux: bare "tmux" when the
// selected tmux is just "tmux" (or found as "tmux" on PATH), else its quoted path.
func (c *Client) attachBinary() string {
	bin := c.resolveTmuxBin()
	if bin == "" || bin == "tmux" {
		return "tmux"
	}
	if p, err := exec.LookPath("tmux"); err == nil && sameFile(p, bin) {
		return "tmux"
	}
	return quoteIfNeeded(bin)
}

// safeShellWord is the set of values that need no quoting in a shell command.
var safeShellWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// quoteIfNeeded leaves plain words as they are (so existing attach commands
// read the same) and passes everything else, including spaces, quotes, newlines
// and any other shell metacharacter, through the always-quoting shellQuote.
func quoteIfNeeded(v string) string {
	// "=", "%" and "@" are plain inside a word, not at its start ("=curl" is curl's path in zsh;
	// #3930, Security's #3927 finding 3).
	if safeShellWord.MatchString(v) && strings.IndexByte("=%@", v[0]) < 0 {
		return v
	}
	return shellQuote(v)
}

// SetPaneTitle sets a tmux pane's title using select-pane -T.
// This is a native tmux command that doesn't go through the shell,
// avoiding printf echo issues.
func (c *Client) SetPaneTitle(ctx context.Context, target, title string) {
	c.run(ctx, "select-pane", "-t", target, "-T", title)
}

// GetPanePID returns the shell PID running in the first pane of the given session.
// PanePIDs is the process of every pane on the socket (#3444).
func (c *Client) PanePIDs(ctx context.Context) ([]int, error) {
	// From the shared pane list (#4346), which has every pane's pid; it ran a list-panes of its own.
	s := c.paneSnapshot(ctx)
	if s == nil {
		return nil, fmt.Errorf("tmux list-panes failed")
	}
	var pids []int
	for _, st := range s.states {
		if st.pid > 0 {
			pids = append(pids, st.pid)
		}
	}
	return pids, nil
}

func (c *Client) GetPanePID(ctx context.Context, sessionName string) (int, error) {
	out, err := c.run(ctx, "list-panes", "-t", sessionName, "-F", "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	line := strings.TrimSpace(strings.Split(out, "\n")[0])
	pid, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("invalid pane pid %q: %w", line, err)
	}
	return pid, nil
}

// ResizePaneTarget resizes a tmux pane by target address (both width and height).
func (c *Client) ResizePaneTarget(ctx context.Context, target string, columns, rows int) error {
	args := []string{"resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns)}
	if rows > 0 {
		args = append(args, "-y", fmt.Sprintf("%d", rows))
	}
	_, err := c.run(ctx, args...)
	return err
}
