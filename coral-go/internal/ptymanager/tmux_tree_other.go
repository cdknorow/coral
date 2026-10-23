//go:build windows

package ptymanager

import (
	"context"

	"github.com/subgentic/coral/internal/tmux"
)

// SetRunTagLookup: tmux does not run on Windows, so there is no tag to look up.
func SetRunTagLookup(func(sessionID string) string) {}

// tmux does not run on Windows: no panes to look at.
func trackTmuxPanes(*tmux.Client) func() { return func() {} }

// tmux does not run on Windows: no tree kill is ever pending.
func finishPendingTreeKills() {}

// tmux does not run on Windows: nothing to end beyond the kill itself.
func paneTreeForAgent(context.Context, *tmux.Client, string, string, string) func() { return func() {} }
func paneTreeForTarget(context.Context, *tmux.Client, string) func()                { return func() {} }
