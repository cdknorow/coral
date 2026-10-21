package ptymanager

import (
	"context"
	"github.com/subgentic/coral/internal/terminalinput"

	"github.com/subgentic/coral/internal/tmux"
)

// TmuxSessionTerminal implements SessionTerminal using tmux.
type TmuxSessionTerminal struct {
	client *tmux.Client
}

// NewTmuxSessionTerminal creates a SessionTerminal backed by tmux.
func NewTmuxSessionTerminal(client *tmux.Client) *TmuxSessionTerminal {
	return &TmuxSessionTerminal{client: client}
}

// Client returns the underlying tmux client for direct access when needed.
func (t *TmuxSessionTerminal) Client() *tmux.Client {
	return t.client
}

func (t *TmuxSessionTerminal) ListSessions(ctx context.Context) ([]PaneInfo, error) {
	panes, err := t.client.ListPanes(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]PaneInfo, len(panes))
	for i, p := range panes {
		sessions[i] = PaneInfo{
			PaneTitle:   p.PaneTitle,
			SessionName: p.SessionName,
			Target:      p.Target,
			CurrentPath: p.CurrentPath,
		}
	}
	return sessions, nil
}

func (t *TmuxSessionTerminal) FindSession(ctx context.Context, name, agentType, sessionID string) (*PaneInfo, error) {
	pane, err := t.client.FindPane(ctx, name, agentType, sessionID)
	if err != nil {
		return nil, err
	}
	if pane == nil {
		return nil, nil
	}
	return &PaneInfo{
		PaneTitle:   pane.PaneTitle,
		SessionName: pane.SessionName,
		Target:      pane.Target,
		CurrentPath: pane.CurrentPath,
	}, nil
}

func (t *TmuxSessionTerminal) CaptureOutput(ctx context.Context, name string, lines int, agentType, sessionID string) (string, error) {
	return t.client.CapturePane(ctx, name, lines, agentType, sessionID)
}

func (t *TmuxSessionTerminal) SendInput(ctx context.Context, o terminalinput.Origin, name, command, agentType, sessionID string) error {
	return t.client.SendKeys(ctx, o, name, command, agentType, sessionID)
}

// SendPrompt delivers text as one bracketed paste followed by Enter.
func (t *TmuxSessionTerminal) SendPrompt(ctx context.Context, o terminalinput.Origin, name, text, agentType, sessionID string) error {
	return t.client.SendPrompt(ctx, o, name, text, agentType, sessionID)
}

func (t *TmuxSessionTerminal) SendRawInput(ctx context.Context, o terminalinput.Origin, name string, keys []string, agentType, sessionID string) error {
	return t.client.SendRawKeys(ctx, o, name, keys, agentType, sessionID)
}

func (t *TmuxSessionTerminal) SendToTarget(ctx context.Context, o terminalinput.Origin, target, command string) error {
	DropHeld(target, "a launch command is typed") // nothing held for the old process is typed into a new one (#3431)
	return t.client.SendKeysToTarget(ctx, o, target, command)
}

func (t *TmuxSessionTerminal) SendTerminalInput(ctx context.Context, o terminalinput.Origin, target, data string) error {
	return t.client.SendTerminalInputToTarget(ctx, o, target, data)
}

func (t *TmuxSessionTerminal) CreateSession(ctx context.Context, name, workDir string) error {
	DropHeld(name, "a new terminal") // nothing held for the old process is typed into a new one (#3431)
	return t.client.NewSession(ctx, name, workDir)
}

func (t *TmuxSessionTerminal) KillSession(ctx context.Context, name, agentType, sessionID string) error {
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	return t.client.KillSession(ctx, name, agentType, sessionID)
}

func (t *TmuxSessionTerminal) KillSessionOnly(ctx context.Context, name, agentType, sessionID string) error {
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	return t.client.KillSessionOnly(ctx, name, agentType, sessionID)
}

func (t *TmuxSessionTerminal) RestartPane(ctx context.Context, target, workDir string) error {
	DropHeld(target, "restarted") // nothing held for the old process is typed into a new one (#3431)
	return t.client.RespawnPane(ctx, target, workDir)
}

func (t *TmuxSessionTerminal) RenameSession(ctx context.Context, oldName, newName string) error {
	return t.client.RenameSession(ctx, oldName, newName)
}

func (t *TmuxSessionTerminal) ResizeSession(ctx context.Context, name string, columns int, agentType, sessionID string) error {
	return t.client.ResizePane(ctx, name, columns, agentType, sessionID)
}

func (t *TmuxSessionTerminal) SetPaneTitle(ctx context.Context, target, title string) {
	t.client.SetPaneTitle(ctx, target, title)
}

func (t *TmuxSessionTerminal) StartLogging(ctx context.Context, target, logPath string) error {
	return t.client.PipePane(ctx, target, logPath)
}

func (t *TmuxSessionTerminal) StopLogging(ctx context.Context, target string) error {
	return t.client.ClosePipePane(ctx, target)
}

func (t *TmuxSessionTerminal) ClearHistory(ctx context.Context, target string) error {
	return t.client.ClearHistory(ctx, target)
}

func (t *TmuxSessionTerminal) HasSession(ctx context.Context, name string) bool {
	return t.client.HasSession(ctx, name)
}

func (t *TmuxSessionTerminal) FindTarget(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return t.client.FindPaneTarget(ctx, name, agentType, sessionID)
}

func (t *TmuxSessionTerminal) AttachCommand(sessionName string) string {
	return t.client.AttachCommand(sessionName)
}
