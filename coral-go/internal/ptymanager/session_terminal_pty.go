package ptymanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/tmux"
)

// PTYSessionTerminal implements SessionTerminal using native PTY sessions.
// Used on Windows (ConPTY) and macOS App Store (no tmux).
type PTYSessionTerminal struct {
	backend *PTYBackend
}

// NewPTYSessionTerminal creates a SessionTerminal backed by PTYBackend.
func NewPTYSessionTerminal(backend *PTYBackend) *PTYSessionTerminal {
	return &PTYSessionTerminal{backend: backend}
}

func (p *PTYSessionTerminal) ListSessions(_ context.Context) ([]PaneInfo, error) {
	sessions := p.backend.ListSessions()
	panes := make([]PaneInfo, len(sessions))
	for i, s := range sessions {
		panes[i] = PaneInfo{
			PaneTitle:   s.AgentName,
			SessionName: naming.SessionName(s.AgentType, s.SessionID),
			Target:      s.SessionID,
			CurrentPath: s.WorkingDir,
		}
	}
	return panes, nil
}

func (p *PTYSessionTerminal) FindSession(_ context.Context, name, agentType, sessionID string) (*PaneInfo, error) {
	if sessionID == "" {
		if _, sid := logparse.ParseSessionName(name); sid != "" {
			sessionID = sid // an exact session name names one session
		}
	}
	sessions := p.backend.ListSessions()
	for _, s := range sessions {
		sessName := naming.SessionName(s.AgentType, s.SessionID)
		// A session id, or else the session's own backend key (AgentName holds it, unique),
		// finds a session; never a folder (#3211, #3704): teammates share folders.
		match := s.SessionID == sessionID
		if sessionID == "" {
			match = s.AgentName == name
		}
		if match {
			return &PaneInfo{
				PaneTitle:   s.AgentName,
				SessionName: sessName,
				Target:      s.SessionID,
				CurrentPath: s.WorkingDir,
			}, nil
		}
	}
	if sessionID == "" {
		tmux.WarnBareNameLookup("pty", name)
	}
	return nil, nil
}

// keyFor is the backend key of the agent's own session: by session id when there is one
// (and ErrTerminalGone when that session is gone: never another session with the same name
// or folder, #3211), else by name for legacy callers.
func (p *PTYSessionTerminal) keyFor(name, sessionID string) (string, error) {
	if sessionID == "" {
		if _, sid := logparse.ParseSessionName(name); sid != "" {
			sessionID = sid // an exact session name names one session
		} else {
			return p.resolveSessionKey(name), nil
		}
	}
	for _, s := range p.backend.ListSessions() {
		if s.SessionID == sessionID {
			return naming.SessionName(s.AgentType, s.SessionID), nil // never a shared name or folder
		}
	}
	return "", fmt.Errorf("%w: session %s", ErrTerminalGone, sessionID)
}

// resolveSessionKey maps a session's key (its session name, "claude-{uuid}") or its session
// id (a pane's Target) to the key PTYBackend uses. Nothing else names a session (#3704, as
// #3687 for tmux): an agent name or folder is shared by teammates, so a match could be
// another agent's terminal. An unknown name is returned as it is, and the backend finds no
// session for it.
func (p *PTYSessionTerminal) resolveSessionKey(name string) string {
	if p.backend.IsRunning(name) {
		return name
	}
	for _, s := range p.backend.ListSessions() {
		if s.SessionID == name {
			return naming.SessionName(s.AgentType, s.SessionID)
		}
	}
	tmux.WarnBareNameLookup("pty", name)
	return name
}

func (p *PTYSessionTerminal) CaptureOutput(_ context.Context, name string, _ int, _, sessionID string) (string, error) {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return "", nil // gone: nothing to show, and never another session's output
	}
	data, err := p.backend.Replay(key)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *PTYSessionTerminal) SendInput(ctx context.Context, name, command, _, sessionID string) error {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	return p.backend.SendText(ctx, key, command)
}

// SendPrompt delivers text as one bracketed paste followed by Enter, only while
// the session's application has bracketed paste mode enabled.
func (p *PTYSessionTerminal) SendPrompt(ctx context.Context, name, text, _, sessionID string) error {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	return p.backend.SendPrompt(ctx, key, text)
}

// SendEnter implements EnterSender: the Enter of a send the automated-input gate
// withheld because a prompt opened after its text (#3431, #3457).
func (p *PTYSessionTerminal) SendEnter(_ context.Context, name, _, sessionID string) error {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	return p.backend.SendInput(key, []byte("\r"))
}

// SendArrow implements ArrowSender: the cursor key's escape sequence (Claude Code's and Codex's input
// readers take it in either cursor-key mode).
func (p *PTYSessionTerminal) SendArrow(_ context.Context, name, _, sessionID string, up bool) error {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	seq := "\x1b[B"
	if up {
		seq = "\x1b[A"
	}
	return p.backend.SendInput(key, []byte(seq))
}

func (p *PTYSessionTerminal) SendRawInput(_ context.Context, name string, keys []string, _, sessionID string) error {
	resolved, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := p.backend.SendInput(resolved, []byte(key)); err != nil {
			return err
		}
	}
	return nil
}

func (p *PTYSessionTerminal) SendToTarget(_ context.Context, target, command string) error {
	DropHeld(target, "a launch command is typed") // nothing held for the old process is typed into a new one (#3431)
	resolved := p.resolveSessionKey(target)
	if len(command) > inlineCommandLimit {
		invocation, cleanup, err := commandInvocation(command)
		if err != nil {
			return err
		}
		if err := p.backend.SendLine(resolved, invocation); err != nil {
			cleanup()
			return err
		}
		return nil
	}
	return p.backend.SendLine(resolved, command)
}

func (p *PTYSessionTerminal) SendTerminalInput(_ context.Context, target, data string) error {
	return p.backend.SendInput(p.resolveSessionKey(target), []byte(data))
}

// HumanInputSince implements HumanInputReader: whether a person typed in the agent's
// terminal after t (the gate's PTY rule, #3482).
func (p *PTYSessionTerminal) HumanInputSince(name, sessionID string, t time.Time) bool {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return false // gone: nobody typed in it
	}
	return p.backend.HumanInputSince(key, t)
}

func (p *PTYSessionTerminal) CreateSession(_ context.Context, name, workDir string) error {
	DropHeld(name, "a new terminal") // nothing held for the old process is typed into a new one (#3431)
	// Parse agent type from session name
	agentType := "agent"
	sessionID := name
	if parts := strings.SplitN(name, "-", 2); len(parts) == 2 && len(parts[1]) >= 36 {
		agentType = parts[0]
		sessionID = parts[1]
	}
	return p.backend.Spawn(name, agentType, workDir, sessionID, "", 200, 50)
}

func (p *PTYSessionTerminal) KillSession(ctx context.Context, name, agentType, sessionID string) error {
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	return p.killByAnyKey(ctx, name, agentType, sessionID)
}

func (p *PTYSessionTerminal) KillSessionOnly(ctx context.Context, name, agentType, sessionID string) error {
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	return p.killByAnyKey(ctx, name, agentType, sessionID)
}

// killByAnyKey kills the agent's own session: by session id or exact session name, or a
// backend key (#3211, #3704: never by an agent name or folder, which would kill another agent).
func (p *PTYSessionTerminal) killByAnyKey(_ context.Context, name, agentType, sessionID string) error {
	if sessionID != "" || isSessionName(name) {
		key, err := p.keyFor(name, sessionID)
		if err != nil {
			return err
		}
		return p.backend.Kill(key)
	}
	if err := p.backend.Kill(p.resolveSessionKey(name)); err != nil {
		return fmt.Errorf("session %q not found", name)
	}
	return nil
}

func isSessionName(name string) bool {
	_, sid := logparse.ParseSessionName(name)
	return sid != ""
}

func (p *PTYSessionTerminal) RestartPane(_ context.Context, target, _ string) error {
	DropHeld(target, "restarted") // nothing held for the old process is typed into a new one (#3431)
	return p.backend.Restart(p.resolveSessionKey(target), "")
}

func (p *PTYSessionTerminal) RenameSession(_ context.Context, _, _ string) error {
	// PTY sessions are tracked by name internally; rename is a no-op
	// since session identity is maintained by the PTYBackend map
	return nil
}

func (p *PTYSessionTerminal) ResizeSession(_ context.Context, name string, columns int, _, sessionID string) error {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return err
	}
	return p.backend.Resize(key, uint16(columns), 50)
}

func (p *PTYSessionTerminal) SetPaneTitle(_ context.Context, _, _ string) {
	// No-op for PTY backend — pane titles are a tmux concept
}

func (p *PTYSessionTerminal) StartLogging(_ context.Context, _, _ string) error {
	// PTY backend already logs via the session's readLoop → logFile writer
	return nil
}

func (p *PTYSessionTerminal) StopLogging(_ context.Context, _ string) error {
	// No-op for PTY — logging is inherent to the session lifecycle
	return nil
}

func (p *PTYSessionTerminal) ClearHistory(_ context.Context, _ string) error {
	// No scrollback history concept in PTY mode (ring buffer auto-manages)
	return nil
}

func (p *PTYSessionTerminal) HasSession(_ context.Context, name string) bool {
	return p.backend.IsRunning(name)
}

func (p *PTYSessionTerminal) FindTarget(_ context.Context, name, agentType, sessionID string) (string, error) {
	pane, err := p.FindSession(context.Background(), name, agentType, sessionID)
	if err != nil {
		return "", err
	}
	if pane == nil {
		return "", nil
	}
	return pane.SessionName, nil
}

func (p *PTYSessionTerminal) AttachCommand(_ string) string {
	return "" // PTY backend doesn't use tmux attach
}

// Verify interface compliance at compile time.
var _ SessionTerminal = (*PTYSessionTerminal)(nil)
var _ SessionTerminal = (*TmuxSessionTerminal)(nil)
