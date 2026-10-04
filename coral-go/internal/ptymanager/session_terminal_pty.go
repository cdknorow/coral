package ptymanager

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cdknorow/coral/internal/naming"
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
	sessions := p.backend.ListSessions()
	for _, s := range sessions {
		sessName := naming.SessionName(s.AgentType, s.SessionID)
		if sessName == name || s.AgentName == name || s.SessionID == sessionID {
			return &PaneInfo{
				PaneTitle:   s.AgentName,
				SessionName: sessName,
				Target:      s.SessionID,
				CurrentPath: s.WorkingDir,
			}, nil
		}
	}
	return nil, nil
}

func (p *PTYSessionTerminal) CaptureOutput(_ context.Context, name string, _ int, _, _ string) (string, error) {
	data, err := p.backend.Replay(name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *PTYSessionTerminal) SendInput(_ context.Context, name, command, _, _ string) error {
	return p.backend.SendInput(name, []byte(command+"\n"))
}

// SendPrompt delivers text as one bracketed paste followed by Enter, only while
// the session's application has bracketed paste mode enabled.
func (p *PTYSessionTerminal) SendPrompt(ctx context.Context, name, text, _, _ string) error {
	return p.backend.SendPrompt(ctx, name, text)
}

func (p *PTYSessionTerminal) SendRawInput(_ context.Context, name string, keys []string, _, _ string) error {
	for _, key := range keys {
		if err := p.backend.SendInput(name, []byte(key)); err != nil {
			return err
		}
	}
	return nil
}

func (p *PTYSessionTerminal) SendToTarget(_ context.Context, target, command string) error {
	// Target is session name in PTY mode
	if len(command) > inlineCommandLimit {
		invocation, cleanup, err := commandInvocation(command)
		if err != nil {
			return err
		}
		if err := p.backend.SendInput(target, []byte(invocation+"\n")); err != nil {
			cleanup()
			return err
		}
		return nil
	}
	return p.backend.SendInput(target, []byte(command+"\n"))
}

func (p *PTYSessionTerminal) SendTerminalInput(_ context.Context, target, data string) error {
	return p.backend.SendInput(target, []byte(data))
}

func (p *PTYSessionTerminal) CreateSession(_ context.Context, name, workDir string) error {
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
	return p.killByAnyKey(ctx, name, agentType, sessionID)
}

func (p *PTYSessionTerminal) KillSessionOnly(ctx context.Context, name, agentType, sessionID string) error {
	return p.killByAnyKey(ctx, name, agentType, sessionID)
}

// killByAnyKey finds a session by name, agentType-sessionID, or sessionID and kills it.
func (p *PTYSessionTerminal) killByAnyKey(_ context.Context, name, agentType, sessionID string) error {
	// Try direct name match first (fastest path)
	if err := p.backend.Kill(name); err == nil {
		return nil
	}
	// Try agentType-sessionID format
	if sessionID != "" {
		composed := naming.SessionName(agentType, sessionID)
		if err := p.backend.Kill(composed); err == nil {
			return nil
		}
		// Try sessionID alone
		if err := p.backend.Kill(sessionID); err == nil {
			return nil
		}
	}
	// Search through all sessions for a matching agent name, session ID, or folder name
	for _, s := range p.backend.ListSessions() {
		sessName := naming.SessionName(s.AgentType, s.SessionID)
		if s.AgentName == name || s.SessionID == sessionID || filepath.Base(s.WorkingDir) == name {
			return p.backend.Kill(sessName)
		}
	}
	return fmt.Errorf("session %q not found", name)
}

func (p *PTYSessionTerminal) RestartPane(_ context.Context, target, _ string) error {
	return p.backend.Restart(target, "")
}

func (p *PTYSessionTerminal) RenameSession(_ context.Context, _, _ string) error {
	// PTY sessions are tracked by name internally; rename is a no-op
	// since session identity is maintained by the PTYBackend map
	return nil
}

func (p *PTYSessionTerminal) ResizeSession(_ context.Context, name string, columns int, _, _ string) error {
	return p.backend.Resize(name, uint16(columns), 50)
}

func (p *PTYSessionTerminal) ResizeTarget(_ context.Context, target string, columns, rows int) error {
	r := uint16(rows)
	if r == 0 {
		r = 50
	}
	return p.backend.Resize(target, uint16(columns), r)
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
