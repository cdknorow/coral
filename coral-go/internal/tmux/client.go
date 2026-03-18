// Package tmux provides a client for interacting with tmux sessions.
package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Pane represents a tmux pane with its metadata.
type Pane struct {
	PaneTitle   string `json:"pane_title"`
	SessionName string `json:"session_name"`
	Target      string `json:"target"`
	CurrentPath string `json:"current_path"`
}

// Client wraps tmux command execution.
type Client struct {
	// TmuxBin is the path to the tmux binary. Defaults to "tmux".
	TmuxBin string
}

// NewClient creates a new tmux Client.
func NewClient() *Client {
	return &Client{TmuxBin: "tmux"}
}

// ListPanes returns all tmux panes with their titles, session names, and targets.
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	stdout, err := c.run(ctx, "list-panes", "-a",
		"-F", "#{pane_title}|#{session_name}|#S:#I.#P|#{pane_current_path}")
	if err != nil {
		return nil, nil // tmux not running is not an error
	}

	var panes []Pane
	for _, line := range strings.Split(stdout, "\n") {
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
		})
	}
	return panes, nil
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
func (c *Client) SendKeysToTarget(ctx context.Context, target, command string) error {
	if strings.Contains(command, "\n") {
		// Multi-line: wrap in bracket paste
		if err := c.sendBracketPasted(ctx, target, command); err != nil {
			return err
		}
	} else {
		// Single-line: send as literal text
		if _, err := c.run(ctx, "send-keys", "-t", target, "-l", command); err != nil {
			return fmt.Errorf("send-keys failed: %w", err)
		}
	}

	// Brief pause for tmux to deliver keystrokes
	time.Sleep(300 * time.Millisecond)

	// Send Enter
	if _, err := c.run(ctx, "send-keys", "-t", target, "Enter"); err != nil {
		return fmt.Errorf("send Enter failed: %w", err)
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
func (c *Client) CapturePaneRawTarget(ctx context.Context, target string, lines int) (string, error) {
	stdout, err := c.run(ctx, "capture-pane", "-t", target, "-p", "-e", fmt.Sprintf("-S-%d", lines))
	if err != nil {
		return "", nil
	}
	return stdout, nil
}

// KillSession kills the tmux session for a given agent.
func (c *Client) KillSession(ctx context.Context, agentName, agentType, sessionID string) error {
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

	// Clean up log file
	if sessionID != "" {
		logDir := os.TempDir()
		logPath := filepath.Join(logDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))
		os.Remove(logPath)

		// Clean up settings temp file
		settingsFile := fmt.Sprintf("/tmp/coral_settings_%s.json", sessionID)
		os.Remove(settingsFile)
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

// NewSession creates a new detached tmux session.
func (c *Client) NewSession(ctx context.Context, name, workDir string) error {
	_, err := c.run(ctx, "new-session", "-d", "-s", name, "-c", workDir)
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

// Bracket paste mode escape sequences
var (
	bracketPasteStart = []string{"-H", "1b", "-H", "5b", "-H", "32", "-H", "30", "-H", "30", "-H", "7e"}
	bracketPasteEnd   = []string{"-H", "1b", "-H", "5b", "-H", "32", "-H", "30", "-H", "31", "-H", "7e"}
)

func (c *Client) sendBracketPasted(ctx context.Context, target, text string) error {
	args := append([]string{"send-keys", "-t", target}, bracketPasteStart...)
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("bracket paste start failed: %w", err)
	}

	if _, err := c.run(ctx, "send-keys", "-t", target, "-l", text); err != nil {
		return fmt.Errorf("send-keys failed: %w", err)
	}

	args = append([]string{"send-keys", "-t", target}, bracketPasteEnd...)
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("bracket paste end failed: %w", err)
	}

	return nil
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

	// Multi-line text: wrap in bracket paste
	if strings.ContainsAny(data, "\n\r") {
		return c.sendBracketPasted(ctx, target, data)
	}

	// Single-line literal text
	_, err := c.run(ctx, "send-keys", "-t", target, "-l", data)
	return err
}

// ResizePaneTarget resizes a tmux pane width by target address.
func (c *Client) ResizePaneTarget(ctx context.Context, target string, columns int) error {
	_, err := c.run(ctx, "resize-window", "-t", target, "-x", fmt.Sprintf("%d", columns))
	return err
}

// run executes a tmux command and returns stdout.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.TmuxBin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
