//go:build windows

package ptymanager

import (
	"context"

	"github.com/subgentic/coral/internal/tmux"
)

// tmux does not run on Windows: no tree kill is ever pending.
func finishPendingTreeKills() {}

// tmux does not run on Windows: nothing to end beyond the kill itself.
func paneTreeForAgent(context.Context, *tmux.Client, string, string, string) func() { return func() {} }
func paneTreeForTarget(context.Context, *tmux.Client, string) func()                { return func() {} }
