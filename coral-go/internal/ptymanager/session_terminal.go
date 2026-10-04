// Package ptymanager provides terminal backend abstractions.
//
// SessionTerminal abstracts all terminal operations used by the HTTP sessions
// handler. It has two implementations: TmuxSessionTerminal (wrapping
// tmux.Client) and PTYSessionTerminal (wrapping PTYBackend).
package ptymanager

import (
	"context"

	"github.com/cdknorow/coral/internal/tmux"
)

// PaneInfo describes a running agent session (backend-agnostic).
type PaneInfo struct {
	PaneTitle   string `json:"pane_title"`
	SessionName string `json:"session_name"`
	Target      string `json:"target"`      // tmux target or PTY process ID
	CurrentPath string `json:"current_path"`
}

// SessionTerminal abstracts all terminal operations for the sessions handler.
// This allows the HTTP layer to work identically with tmux or PTY backends.
// PromptSender is implemented by terminal backends that can deliver a
// multi-line prompt to an agent as ONE logical input (a single bracketed paste
// followed by Enter). It returns ErrBracketedPasteUnavailable instead of
// risking early submission when the agent is not accepting bracketed paste.
type PromptSender interface {
	SendPrompt(ctx context.Context, name, text, agentType, sessionID string) error
}

// ErrBracketedPasteUnavailable is returned by PromptSender when the target is
// not accepting bracketed paste.
var ErrBracketedPasteUnavailable = tmux.ErrBracketedPasteUnavailable

// ErrDeliveryUnknown is returned (wrapped) by PromptSender when input may have
// reached the agent before a failure. It must not be retried.
var ErrDeliveryUnknown = tmux.ErrDeliveryUnknown

// WithBracketedPasteHint passes what the caller knows about the target's
// bracketed paste mode to SendPrompt (used by the tmux backend, whose panes
// expose no way to query it). The PTY backend tracks the mode itself.
func WithBracketedPasteHint(ctx context.Context, enabled bool) context.Context {
	return tmux.WithBracketedPasteHint(ctx, enabled)
}

// BracketedPasteHint reads a hint set with WithBracketedPasteHint.
func BracketedPasteHint(ctx context.Context) (enabled, known bool) {
	return tmux.BracketedPasteHint(ctx)
}

type SessionTerminal interface {
	// Discovery
	ListSessions(ctx context.Context) ([]PaneInfo, error)
	FindSession(ctx context.Context, name, agentType, sessionID string) (*PaneInfo, error)

	// Output capture
	CaptureOutput(ctx context.Context, name string, lines int, agentType, sessionID string) (string, error)

	// Input
	SendInput(ctx context.Context, name, command, agentType, sessionID string) error
	SendRawInput(ctx context.Context, name string, keys []string, agentType, sessionID string) error
	SendToTarget(ctx context.Context, target, command string) error
	SendTerminalInput(ctx context.Context, target, data string) error

	// Lifecycle
	CreateSession(ctx context.Context, name, workDir string) error
	KillSession(ctx context.Context, name, agentType, sessionID string) error
	KillSessionOnly(ctx context.Context, name, agentType, sessionID string) error
	RestartPane(ctx context.Context, target, workDir string) error
	RenameSession(ctx context.Context, oldName, newName string) error
	ResizeSession(ctx context.Context, name string, columns int, agentType, sessionID string) error
	ResizeTarget(ctx context.Context, target string, columns, rows int) error

	// Logging
	StartLogging(ctx context.Context, target, logPath string) error
	StopLogging(ctx context.Context, target string) error
	ClearHistory(ctx context.Context, target string) error

	// Pane title (native tmux command, avoids shell echo)
	SetPaneTitle(ctx context.Context, target, title string)

	// Query
	HasSession(ctx context.Context, name string) bool

	// Target-level operations
	FindTarget(ctx context.Context, name, agentType, sessionID string) (string, error)

	// AttachCommand returns the shell command to attach to a session (includes -S socket if needed).
	AttachCommand(sessionName string) string
}
