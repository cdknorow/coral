package ptymanager

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
)

// PTYBackend implements TerminalBackend using native PTY sessions.
type PTYBackend struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

// NewPTYBackend creates a new PTY-based terminal backend.
func NewPTYBackend() *PTYBackend {
	return &PTYBackend{
		sessions: make(map[string]*session),
	}
}

func (m *PTYBackend) Spawn(name, agentType, workDir, sessionID, command string, cols, rows uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[name]; exists {
		return fmt.Errorf("session %q already exists", name)
	}

	s, err := newSession(name, agentType, workDir, sessionID, command, cols, rows)
	if err != nil {
		return err
	}

	m.sessions[name] = s
	return nil
}

func (m *PTYBackend) Kill(name string) error {
	m.mu.Lock()
	s, ok := m.sessions[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %q not found", name)
	}
	delete(m.sessions, name)
	m.mu.Unlock()

	return s.kill()
}

func (m *PTYBackend) Restart(name, command string) error {
	m.mu.RLock()
	old, ok := m.sessions[name]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %q not found", name)
	}

	agentType := old.agentType
	workDir := old.workingDir
	sessionID := old.sessionID

	// Kill old session
	m.mu.Lock()
	delete(m.sessions, name)
	m.mu.Unlock()
	old.kill()

	// Spawn new session with default terminal size
	return m.Spawn(name, agentType, workDir, sessionID, command, 200, 50)
}

func (m *PTYBackend) SendInput(name string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	return s.sendInput(data)
}

// promptSubmitDelay separates a bracketed paste from the submitting Enter.
var promptSubmitDelay = 150 * time.Millisecond

// SendPrompt writes text to a session as ONE logical input: a single bracketed
// paste (ESC[200~ ... ESC[201~) followed by Enter. It only does so while the
// application has enabled bracketed paste; otherwise the raw newlines would be
// read as separate submissions, so it returns ErrBracketedPasteUnavailable
// without writing anything. ESC and other control characters are removed from
// the text so it cannot end the paste early or inject input.
//
// Retry safety: whole prompts are serialized per session, and ctx is honoured
// only BEFORE the first byte is written (nothing sent, safe to retry). After
// the paste starts, the delay and Enter always complete, and any short write or
// error from then on is returned as ErrDeliveryUnknown, which must not be
// retried because the text may already sit in the agent's input line.
func (m *PTYBackend) SendPrompt(ctx context.Context, name, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	s.promptSemOnce.Do(func() { s.promptSem = make(chan struct{}, 1) })
	select {
	case s.promptSem <- struct{}{}:
		defer func() { <-s.promptSem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.bracketedPaste.Load() {
		return ErrBracketedPasteUnavailable
	}
	payload := []byte("\x1b[200~" + tmux.SanitizePaste(text) + "\x1b[201~")
	n, err := s.writeFull(payload)
	if err != nil {
		if n > 0 {
			return tmux.MarkDeliveryUnknown(fmt.Errorf("paste write failed after %d of %d bytes: %w", n, len(payload), err))
		}
		return fmt.Errorf("write prompt: %w", err) // nothing reached the PTY
	}
	// Input has started: finish regardless of ctx, bounded by the fixed delay.
	time.Sleep(promptSubmitDelay)
	if n, err := s.writeFull([]byte("\r")); err != nil || n != 1 {
		if err == nil {
			err = io.ErrShortWrite
		}
		return tmux.MarkDeliveryUnknown(fmt.Errorf("pasted the prompt but sending Enter failed: %w", err))
	}
	return nil
}

// WaitReady blocks until the session's shell has produced output (prompt ready)
// or the timeout expires. Returns true if ready, false on timeout.
func (m *PTYBackend) WaitReady(name string, timeout time.Duration) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.ringMu.Lock()
		n := len(s.ring)
		s.ringMu.Unlock()
		if n > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (m *PTYBackend) Resize(name string, cols, rows uint16) error {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	return s.resize(cols, rows)
}

func (m *PTYBackend) Attach(name, subscriberID string) (<-chan []byte, error) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session %q not found", name)
	}
	return s.subscribe(subscriberID), nil
}

func (m *PTYBackend) Unsubscribe(name, subscriberID string) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return
	}
	s.unsubscribe(subscriberID)
}

func (m *PTYBackend) Replay(name string) ([]byte, error) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session %q not found", name)
	}
	return s.replayBytes(), nil
}

func (m *PTYBackend) ListSessions() []SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, SessionInfo{
			AgentName:  s.name,
			AgentType:  s.agentType,
			SessionID:  s.sessionID,
			WorkingDir: s.workingDir,
			Running:    s.isRunning(),
		})
	}
	return infos
}

func (m *PTYBackend) IsRunning(name string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return s.isRunning()
}

func (m *PTYBackend) LogPath(name string) string {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return ""
	}
	return s.logPath
}

func (m *PTYBackend) Close() error {
	m.mu.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*session)
	m.mu.Unlock()

	for _, s := range sessions {
		s.kill()
	}
	return nil
}
