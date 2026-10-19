package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CapturePane captures the current content of a tmux pane.
func (c *Client) CapturePane(ctx context.Context, agentName string, lines int, agentType, sessionID string) (string, error) {
	return c.capturePane(ctx, agentName, lines, agentType, sessionID)
}

// CapturePaneStyled is CapturePane with each cell's style kept as SGR sequences
// (capture-pane -e): the automated-input gate tells Claude's faint prompt suggestion
// from text a person typed by it (#3468).
func (c *Client) CapturePaneStyled(ctx context.Context, agentName string, lines int, agentType, sessionID string) (string, error) {
	return c.capturePane(ctx, agentName, lines, agentType, sessionID, "-e")
}

func (c *Client) capturePane(ctx context.Context, agentName string, lines int, agentType, sessionID string, flags ...string) (string, error) {
	key := agentName + "\x00" + agentType + "\x00" + sessionID
	args := func(target string) []string {
		return append([]string{"capture-pane", "-t", target, "-p", fmt.Sprintf("-S-%d", lines)}, flags...)
	}
	if sessionID != "" {
		if cached, ok := c.captureTarget(key); ok {
			if out, err := c.run(ctx, args(cached)...); err == nil {
				return out, nil
			}
			c.setCaptureTarget(key, "") // the pane is gone or moved: look it up again
		}
	}
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", nil
	}
	out, err := c.run(ctx, args(target)...)
	if err != nil {
		return "", nil
	}
	if sessionID != "" {
		c.setCaptureTarget(key, target)
	}
	return out, nil
}

// captureTargetsMax caps the target cache; it starts over when full (many short-lived agents).
const captureTargetsMax = 256

func (c *Client) captureTarget(key string) (string, bool) {
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	t, ok := c.captureTargets[key]
	return t, ok
}

// setCaptureTarget caches target for key, or forgets key when target is "".
func (c *Client) setCaptureTarget(key, target string) {
	if target == "" {
		// The pane is gone (a failed capture): the shared pane list may still show it, so the
		// next lookup lists the panes again (#4049).
		c.forgetPaneStates()
	}
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	if target == "" {
		delete(c.captureTargets, key)
		return
	}
	if c.captureTargets == nil || len(c.captureTargets) >= captureTargetsMax {
		c.captureTargets = map[string]string{}
	}
	c.captureTargets[key] = target
}

// forgetCaptureTargets drops the cached targets of a session that is being killed.
func (c *Client) forgetCaptureTargets(sessionID string) {
	if sessionID == "" {
		return
	}
	c.captureTargetsMu.Lock()
	defer c.captureTargetsMu.Unlock()
	for k := range c.captureTargets {
		if strings.HasSuffix(k, "\x00"+sessionID) {
			delete(c.captureTargets, k)
		}
	}
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

// DisplayForAgent is DisplayMessage on an agent's own pane, found through the pane-target
// cache capturePane keeps (#3728): one tmux run while the pane stays where it was, instead of
// listing every pane (twice with the default-socket fallback) first. It returns ErrTerminalGone
// when the pane is gone.
func (c *Client) DisplayForAgent(ctx context.Context, agentName, agentType, sessionID, format string) (string, error) {
	key := agentName + "\x00" + agentType + "\x00" + sessionID
	if sessionID != "" {
		if cached, ok := c.captureTarget(key); ok {
			// tmux answers a target that is gone with nothing and success: empty is checked.
			if out, err := c.DisplayMessage(ctx, cached, format); err == nil && strings.TrimSpace(out) != "" {
				return out, nil
			}
			c.setCaptureTarget(key, "") // the pane is gone or moved (or said nothing): look it up again
		}
	}
	target, err := c.FindPaneTarget(ctx, agentName, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", fmt.Errorf("%w: pane %q not found in any tmux session", ErrTerminalGone, agentName)
	}
	out, err := c.DisplayMessage(ctx, target, format)
	if err != nil {
		return "", c.GoneAfter(ctx, agentName, agentType, sessionID, err)
	}
	if strings.TrimSpace(out) == "" {
		// Gone since the shared pane list was taken (tmux says nothing, with success), or a
		// format that is empty for this pane: only a fresh lookup tells them apart.
		if err := c.GoneAfter(ctx, agentName, agentType, sessionID, errEmptyDisplay); errors.Is(err, ErrTerminalGone) {
			return "", err
		}
	}
	if sessionID != "" {
		c.setCaptureTarget(key, target)
	}
	return out, nil
}
