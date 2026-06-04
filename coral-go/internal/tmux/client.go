// Package tmux provides a client for interacting with tmux sessions.
package tmux

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/naming"
)

// Pane represents a tmux pane with its metadata.
type Pane struct {
	PaneTitle   string `json:"pane_title"`
	SessionName string `json:"session_name"`
	Target      string `json:"target"`
	CurrentPath string `json:"current_path"`
	SocketPath  string `json:"-"` // which tmux socket this pane was found on (empty = default)
}

// Client wraps tmux command execution.
type Client struct {
	// TmuxBin is the path to the tmux binary. Defaults to "tmux".
	// Read/write through resolveTmuxBin() so a user-installed tmux is picked
	// up without restarting the server.
	TmuxBin   string
	tmuxBinMu sync.Mutex
	// SocketPath is an explicit tmux socket path (-S flag).
	// If set, all commands use this socket for consistent session visibility
	// across different launch contexts (terminal vs app bundle).
	SocketPath string
	// FallbackToDefault enables checking the default tmux socket (no -S flag)
	// when no sessions are found on the primary socket. This provides backward
	// compatibility with sessions created before the fixed socket was introduced.
	FallbackToDefault bool
	// sessionSockets caches which socket each session was found on.
	// Populated by ListPanes, used by runForTarget to route commands.
	// Protected by socketsMu since multiple goroutines access this map.
	sessionSockets map[string]string
	socketsMu      sync.RWMutex
}

// resolveTmuxBin returns the path used to invoke tmux, re-checking the
// filesystem if the cached binary isn't reachable. This lets a tmux that
// was installed after the server started be picked up on the next command,
// rather than requiring a restart.
func (c *Client) resolveTmuxBin() string {
	c.tmuxBinMu.Lock()
	defer c.tmuxBinMu.Unlock()
	original := c.TmuxBin
	// Absolute path that still exists — keep using it.
	if filepath.IsAbs(c.TmuxBin) {
		_, err := os.Stat(c.TmuxBin)
		if err == nil {
			return c.TmuxBin
		}
		log.Printf("[tmux] cached absolute tmux path is not usable: path=%q err=%v", c.TmuxBin, err)
	} else if _, err := exec.LookPath(c.TmuxBin); err == nil {
		return c.TmuxBin
	} else {
		log.Printf("[tmux] cached tmux binary not found on PATH: bin=%q path=%q err=%v", c.TmuxBin, os.Getenv("PATH"), err)
	}
	// Cached path no longer works — re-search common locations.
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
		if p != original {
			log.Printf("[tmux] resolved tmux binary: previous=%q resolved=%q path=%q", original, p, os.Getenv("PATH"))
		}
		return p
	}
	log.Printf("[tmux] tmux discovery failed; using unresolved binary: bin=%q path=%q shell=%q coral_tmux_bin=%q", c.TmuxBin, os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"))
	return c.TmuxBin
}

// commonTmuxPaths are checked when tmux is not on PATH (native app bundles
// may not inherit a shell PATH that includes Homebrew).
var commonTmuxPaths = []string{
	"/opt/homebrew/bin/tmux",
	"/usr/local/bin/tmux",
	"/usr/bin/tmux",
	"/opt/local/bin/tmux",
	"/nix/var/nix/profiles/default/bin/tmux",
}

// IsAvailable reports whether tmux can be found on PATH or in a common
// install location. The returned path is the resolved binary (or "tmux"
// if it was found on PATH).
func IsAvailable() (string, bool) {
	if p, err := exec.LookPath("tmux"); err == nil {
		log.Printf("[tmux] discovery found tmux on PATH: %s", p)
		return p, true
	}
	if p := tmuxFromEnv(); p != "" {
		log.Printf("[tmux] discovery found tmux via CORAL_TMUX_BIN: %s", p)
		return p, true
	}
	for _, p := range commonTmuxPaths {
		if executableFileExists(p) {
			log.Printf("[tmux] discovery found tmux in common path: %s", p)
			return p, true
		}
	}
	if p := tmuxFromLoginShell(); p != "" {
		log.Printf("[tmux] discovery found tmux via login shell: %s", p)
		return p, true
	}
	log.Printf("[tmux] discovery failed: path=%q shell=%q coral_tmux_bin=%q common_paths=%v", os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"), commonTmuxPaths)
	return "", false
}

func tmuxFromEnv() string {
	p := strings.TrimSpace(os.Getenv("CORAL_TMUX_BIN"))
	if p == "" {
		return ""
	}
	if resolved, err := exec.LookPath(p); err == nil {
		return resolved
	}
	if executableFileExists(p) {
		return p
	}
	log.Printf("[tmux] CORAL_TMUX_BIN is set but not executable: %q", p)
	return ""
}

func tmuxFromLoginShell() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	shells := []string{}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		shells = append(shells, shell)
	}
	shells = append(shells, "/bin/zsh", "/bin/bash", "/bin/sh")

	seen := make(map[string]bool, len(shells))
	for _, shell := range shells {
		if shell == "" || seen[shell] || !executableFileExists(shell) {
			continue
		}
		seen[shell] = true

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, shell, "-lc", "command -v tmux").Output()
		cancel()
		if err != nil {
			log.Printf("[tmux] login shell tmux lookup failed: shell=%q err=%v", shell, err)
			continue
		}
		p := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if p == "" {
			continue
		}
		if resolved, err := exec.LookPath(p); err == nil {
			return resolved
		}
		if executableFileExists(p) {
			return p
		}
	}
	return ""
}

func executableFileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode()&0111 != 0
}

// NewClient creates a new tmux Client.
// coralDir is the root data directory (e.g. ~/.coral). The tmux socket is
// placed at <coralDir>/tmux.sock. Pass "" to use CORAL_TMUX_SOCKET env var
// or fall back to ~/.coral/tmux.sock.
func NewClient(coralDir ...string) *Client {
	c := &Client{TmuxBin: "tmux", FallbackToDefault: true, sessionSockets: make(map[string]string)}

	// Find tmux binary if not on PATH (native app may not have Homebrew paths).
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
	}

	if len(coralDir) > 0 && coralDir[0] != "" {
		c.SocketPath = filepath.Join(coralDir[0], "tmux.sock")
	} else if sp := os.Getenv("CORAL_TMUX_SOCKET"); sp != "" {
		c.SocketPath = sp
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			c.SocketPath = filepath.Join(home, ".coral", "tmux.sock")
		}
	}
	log.Printf("[tmux] NewClient: tmux_bin=%q socket=%q fallback=%v path=%q shell=%q coral_tmux_bin=%q", c.TmuxBin, c.SocketPath, c.FallbackToDefault, os.Getenv("PATH"), os.Getenv("SHELL"), os.Getenv("CORAL_TMUX_BIN"))
	return c
}

// ListPanes returns all tmux panes with their titles, session names, and targets.
// If FallbackToDefault is enabled and the primary socket has no sessions,
// also checks the default tmux socket for backward compatibility.
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	panes := c.listPanesOnSocket(ctx, c.SocketPath)

	// Always merge sessions from the default socket for backward compatibility
	if c.FallbackToDefault && c.SocketPath != "" {
		fallbackPanes := c.listPanesOnSocket(ctx, "")
		// Deduplicate by session name (prefer primary socket)
		seen := make(map[string]bool)
		for _, p := range panes {
			seen[p.SessionName] = true
		}
		for _, p := range fallbackPanes {
			if !seen[p.SessionName] {
				panes = append(panes, p)
			}
		}
	}

	// Cache which socket each session lives on
	c.socketsMu.Lock()
	for _, p := range panes {
		c.sessionSockets[p.SessionName] = p.SocketPath
	}
	c.socketsMu.Unlock()

	return panes, nil
}

func (c *Client) listPanesOnSocket(ctx context.Context, socketPath string) []Pane {
	args := []string{"list-panes", "-a", "-F", "#{pane_title}|#{session_name}|#S:#I.#P|#{pane_current_path}"}
	if socketPath != "" {
		args = append([]string{"-S", socketPath}, args...)
	}
	cmd := exec.CommandContext(ctx, c.resolveTmuxBin(), args...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var panes []Pane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		panes = append(panes, Pane{
			PaneTitle:   parts[0],
			SessionName: parts[1],
			Target:      parts[2],
			CurrentPath: parts[3],
			SocketPath:  socketPath,
		})
	}
	return panes
}

// FindPane finds the tmux pane for a given agent, matching by session_id or agent_name.
func (c *Client) FindPane(ctx context.Context, agentName string, agentType string, sessionID string) (*Pane, error) {
	panes, err := c.ListPanes(ctx)
	if err != nil {
		return nil, err
	}

	// Fast path: match by session_id in tmux session name
	if sessionID != "" {
		sidLow := strings.ToLower(sessionID)
		for i := range panes {
			if strings.Contains(strings.ToLower(panes[i].SessionName), sidLow) {
				return &panes[i], nil
			}
		}
	}

	// Fallback: fuzzy match by agent_name
	agentLow := strings.ToLower(agentName)
	normName := strings.ToLower(strings.ReplaceAll(agentName, "_", "-"))
	typeLow := strings.ToLower(agentType)

	var fallback *Pane
	for i := range panes {
		titleLow := strings.ToLower(panes[i].PaneTitle)
		sessLow := strings.ToLower(panes[i].SessionName)
		pathBase := strings.ToLower(filepath.Base(strings.TrimRight(panes[i].CurrentPath, "/")))

		nameMatch := strings.Contains(titleLow, agentLow) ||
			strings.Contains(titleLow, normName) ||
			strings.Contains(sessLow, agentLow) ||
			strings.Contains(sessLow, normName) ||
			agentLow == pathBase ||
			normName == pathBase

		if !nameMatch {
			continue
		}

		if typeLow != "" {
			if strings.Contains(titleLow, typeLow) || strings.Contains(sessLow, typeLow) {
				return &panes[i], nil
			}
			if fallback == nil {
				p := panes[i]
				fallback = &p
			}
		} else {
			return &panes[i], nil
		}
	}

	return fallback, nil
}

// FindPaneTarget returns the tmux target address for a given agent.
func (c *Client) FindPaneTarget(ctx context.Context, agentName, agentType, sessionID string) (string, error) {
	pane, err := c.FindPane(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if pane == nil {
		return "", nil
	}
	return pane.Target, nil
}

// SendKeys sends a command to a tmux pane. Multi-line text is wrapped in bracket paste.
func (c *Client) SendKeys(ctx context.Context, agentName, command, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("pane %q not found in any tmux session", agentName)
	}

	return c.SendKeysToTarget(ctx, target, command)
}

// SendKeysToTarget sends a command to a specific tmux target.
// Multi-line text uses tmux paste-buffer -p which respects the pane's
// bracketed paste mode — if the shell has it enabled, tmux wraps the
// paste in \e[200~...\e[201~; otherwise it sends raw text.
func (c *Client) SendKeysToTarget(ctx context.Context, target, command string) error {
	var sendErr error
	if strings.Contains(command, "\n") {
		sendErr = c.pasteToTarget(ctx, target, command)
	} else {
		_, sendErr = c.run(ctx, "send-keys", "-t", target, "-l", command)
	}
	if sendErr != nil {
		log.Printf("[tmux] send-keys error (target=%s, cmd_len=%d): %v", target, len(command), sendErr)
	}

	// Brief pause for tmux to deliver keystrokes
	time.Sleep(300 * time.Millisecond)

	// Always send Enter even if send-keys reported an error — the text
	// may have been delivered despite a non-zero exit from tmux.
	if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
		if sendErr != nil {
			return fmt.Errorf("send-keys failed: %w; enter also failed: %v", sendErr, err)
		}
		return fmt.Errorf("send Enter failed: %w", err)
	}
	return sendErr
}

// pasteToTarget loads text into a tmux buffer and pastes it into the target pane.
// Uses paste-buffer -p which respects the pane's bracketed paste mode setting.
func (c *Client) pasteToTarget(ctx context.Context, target, text string) error {
	// Load text into tmux buffer via stdin
	args := []string{"load-buffer", "-"}
	socketPath := c.SocketPath
	if socketPath != "" {
		args = append([]string{"-S", socketPath}, args...)
	}
	cmd := exec.CommandContext(ctx, c.resolveTmuxBin(), args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("load-buffer failed: %w", err)
	}

	// Paste buffer into the target pane (-p = bracket paste aware)
	if _, err := c.run(ctx, "paste-buffer", "-t", target, "-p"); err != nil {
		return fmt.Errorf("paste-buffer failed: %w", err)
	}
	return nil
}

// SendRawKeys sends raw tmux key names (e.g. "BTab", "Escape") to a pane.
func (c *Client) SendRawKeys(ctx context.Context, agentName string, keys []string, agentType, sessionID string) error {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("pane %q not found in any tmux session", agentName)
	}

	for _, key := range keys {
		if _, err := c.run(ctx, "send-keys", "-t", target, key); err != nil {
			return fmt.Errorf("send-keys %q failed: %w", key, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// CapturePane captures the current content of a tmux pane.
func (c *Client) CapturePane(ctx context.Context, agentName string, lines int, agentType, sessionID string) (string, error) {
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", nil
	}

	return c.CapturePaneTarget(ctx, target, lines)
}

// CapturePaneTarget captures pane content by target address.
func (c *Client) CapturePaneTarget(ctx context.Context, target string, lines int) (string, error) {
	stdout, err := c.run(ctx, "capture-pane", "-t", target, "-p", fmt.Sprintf("-S-%d", lines))
	if err != nil {
		return "", nil
	}
	return stdout, nil
}

// CapturePaneRawTarget captures pane content with ANSI sequences preserved.
// If visibleOnly is true, captures only the visible viewport (no scrollback).
// This is needed for TUI apps (vim, nano) that use the alternate screen buffer.
func (c *Client) CapturePaneRawTarget(ctx context.Context, target string, lines int, visibleOnly ...bool) (string, error) {
	args := []string{"capture-pane", "-t", target, "-p", "-e"}
	if len(visibleOnly) == 0 || !visibleOnly[0] {
		args = append(args, fmt.Sprintf("-S-%d", lines))
	}
	stdout, err := c.run(ctx, args...)
	if err != nil {
		return "", nil
	}
	return stdout, nil
}

// DisplayMessage runs tmux display-message to query pane state variables.
// Returns the formatted output string.
func (c *Client) DisplayMessage(ctx context.Context, target, format string) (string, error) {
	return c.run(ctx, "display-message", "-t", target, "-p", format)
}

// KillSession kills the tmux session for a given agent.
func (c *Client) KillSession(ctx context.Context, agentName, agentType, sessionID string) error {
	if err := c.killTmuxSession(ctx, agentName, agentType, sessionID); err != nil {
		return err
	}

	// Clean up log and settings files
	if sessionID != "" {
		logDir := os.TempDir()
		logPath := naming.LogFile(logDir, agentType, sessionID)
		os.Remove(logPath)

		settingsFile := filepath.Join(os.TempDir(), fmt.Sprintf("coral_settings_%s.json", sessionID))
		os.Remove(settingsFile)
	}

	return nil
}

// KillSessionOnly kills a tmux session without cleaning up log/settings files.
// Used by sleep to preserve state for later wake.
func (c *Client) KillSessionOnly(ctx context.Context, agentName, agentType, sessionID string) error {
	return c.killTmuxSession(ctx, agentName, agentType, sessionID)
}

// killTmuxSession finds and kills a tmux session by agent identity.
func (c *Client) killTmuxSession(ctx context.Context, agentName, agentType, sessionID string) error {
	pane, err := c.FindPane(ctx, agentName, agentType, sessionID)
	if err != nil {
		return err
	}
	if pane == nil {
		return fmt.Errorf("pane %q not found in any tmux session", agentName)
	}

	_, err = c.run(ctx, "kill-session", "-t", pane.SessionName)
	if err != nil {
		return fmt.Errorf("kill-session failed: %w", err)
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
		return fmt.Errorf("pane %q not found in any tmux session", agentName)
	}

	_, err = c.run(ctx, "resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns))
	return err
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
	log.Printf("[tmux] NewSession requested: name=%q work_dir=%q tmux_bin=%q socket=%q path=%q", name, workDir, c.TmuxBin, c.SocketPath, os.Getenv("PATH"))
	_, err := c.run(ctx, "new-session", "-d", "-s", name, "-c", workDir)
	if err != nil {
		return err
	}
	c.run(ctx, "set-option", "-t", name, "history-limit", "50000")
	return nil
}

// SetEnvironment sets an environment variable for a tmux session.
func (c *Client) SetEnvironment(ctx context.Context, session, key, value string) error {
	_, err := c.run(ctx, "set-environment", "-t", session, key, value)
	return err
}

// PipePane sets up pipe-pane logging for a tmux session.
func (c *Client) PipePane(ctx context.Context, target, logFile string) error {
	_, err := c.run(ctx, "pipe-pane", "-t", target, "-o", fmt.Sprintf("cat >> '%s'", logFile))
	return err
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
	_, err := c.run(ctx, args...)
	return err
}

// ClearHistory clears the tmux pane scrollback history.
func (c *Client) ClearHistory(ctx context.Context, target string) error {
	_, err := c.run(ctx, "clear-history", "-t", target)
	return err
}

// SendTerminalInputToTarget sends raw terminal input data to a resolved tmux target.
// Handles control characters, escape sequences, and multi-line text.
func (c *Client) SendTerminalInputToTarget(ctx context.Context, target, data string) error {
	// Control character map
	ctrlMap := map[string]string{
		"\r":   "Enter",
		"\x7f": "BSpace",
		"\x1b": "Escape",
		"\t":   "Tab",
	}

	// Single control character
	if len(data) == 1 {
		if key, ok := ctrlMap[data]; ok {
			_, err := c.run(ctx, "send-keys", "-t", target, key)
			return err
		}
		// Ctrl+<letter> (0x01-0x1a)
		b := data[0]
		if b >= 1 && b <= 26 {
			key := fmt.Sprintf("C-%c", b+96)
			_, err := c.run(ctx, "send-keys", "-t", target, key)
			return err
		}
	}

	// Escape sequences (arrow keys, function keys, etc.)
	if len(data) > 0 && data[0] == '\x1b' {
		hexArgs := []string{"send-keys", "-t", target}
		for _, b := range []byte(data) {
			hexArgs = append(hexArgs, "-H", fmt.Sprintf("%02x", b))
		}
		_, err := c.run(ctx, hexArgs...)
		return err
	}

	// Multi-line text: use tmux paste-buffer -p (bracket paste aware)
	if strings.ContainsAny(data, "\n\r") {
		return c.pasteToTarget(ctx, target, data)
	}

	// Single-line literal text
	_, err := c.run(ctx, "send-keys", "-t", target, "-l", data)
	return err
}

// AttachCommand returns the full tmux attach command string including -S flag
// if a custom socket path is configured. Used for display and osascript.
func (c *Client) AttachCommand(sessionName string) string {
	if c.SocketPath != "" {
		return fmt.Sprintf("tmux -S %s attach -t %s", c.SocketPath, sessionName)
	}
	return fmt.Sprintf("tmux attach -t %s", sessionName)
}

// SetPaneTitle sets a tmux pane's title using select-pane -T.
// This is a native tmux command that doesn't go through the shell,
// avoiding printf echo issues.
func (c *Client) SetPaneTitle(ctx context.Context, target, title string) {
	c.run(ctx, "select-pane", "-t", target, "-T", title)
}

// GetPanePID returns the shell PID running in the first pane of the given session.
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

// run executes a tmux command. If the args target a session (-t flag),
// it routes to the correct socket using the session cache.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	// Check if this command targets a session — route via cache
	if c.FallbackToDefault && c.SocketPath != "" {
		for i, arg := range args {
			if arg == "-t" && i+1 < len(args) {
				target := args[i+1]
				session := sessionFromTarget(target)
				c.socketsMu.RLock()
				socket, ok := c.sessionSockets[session]
				c.socketsMu.RUnlock()
				if ok && socket != c.SocketPath {
					return c.runOnSocket(ctx, socket, args...)
				}
				break
			}
		}
	}
	// Default: use primary socket, fallback on error
	out, err := c.runOnSocket(ctx, c.SocketPath, args...)
	if err != nil && c.FallbackToDefault && c.SocketPath != "" {
		if fallbackOut, fallbackErr := c.runOnSocket(ctx, "", args...); fallbackErr == nil {
			return fallbackOut, nil
		}
	}
	return out, err
}

// runOnSocket runs a tmux command on a specific socket. Empty socketPath uses the default.
func (c *Client) runOnSocket(ctx context.Context, socketPath string, args ...string) (string, error) {
	if socketPath != "" {
		args = append([]string{"-S", socketPath}, args...)
	}
	bin := c.resolveTmuxBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[tmux] exec failed: bin=%q socket=%q args=%q err=%v", bin, socketPath, args, err)
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sessionFromTarget extracts the session name from a tmux target (e.g. "name.0" → "name").
func sessionFromTarget(target string) string {
	if i := strings.IndexAny(target, ".:"); i > 0 {
		return target[:i]
	}
	return target
}
