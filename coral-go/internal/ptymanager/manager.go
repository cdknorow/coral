package ptymanager

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/tmux"
)

// PTYBackend implements TerminalBackend using native PTY sessions.
type PTYBackend struct {
	mu       sync.RWMutex
	sessions map[string]*session
	// humanAt: when a person last typed in each session's terminal (SendHumanInput),
	// for the gate's PTY rule: the box can't be read from raw output (#3482, P3).
	humanAt map[string]time.Time
}

// NewPTYBackend creates a new PTY-based terminal backend.
func NewPTYBackend() *PTYBackend {
	return &PTYBackend{
		sessions: make(map[string]*session),
	}
}

func (m *PTYBackend) Spawn(name, agentType, workDir, sessionID, command string, cols, rows uint16) error {
	DropHeld(name, "spawned") // nothing held for the old process is typed into a new one (#3431)
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

func (m *PTYBackend) Kill(name string) error { return m.KillWithin(name, 5*time.Second) }

// KillWithin is Kill with the time the process is given to exit on SIGTERM before it is force-killed: a restart or a resume
// ends the old terminal with a short one (an agent that ignores SIGTERM held every pty restart for the full 5 s, #4842).
func (m *PTYBackend) KillWithin(name string, grace time.Duration) error {
	DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	m.mu.Lock()
	s, ok := m.sessions[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
	}
	delete(m.sessions, name)
	m.mu.Unlock()

	return s.killWithin(grace)
}

func (m *PTYBackend) Restart(name, command string) error {
	DropHeld(name, "restarted") // nothing held for the old process is typed into a new one (#3431)
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

// HumanInputSender is implemented by backends that tell a person's keystrokes (the
// dashboard's terminal) from Coral's own input.
type HumanInputSender interface {
	SendHumanInput(name string, data []byte) error
}

const (
	// humanAtKeysMax is the size past which SendHumanInput forgets old entries, and
	// humanAtForgetAfter how old: far longer than any "typed since" question asks (#3999).
	humanAtKeysMax     = 256
	humanAtForgetAfter = time.Hour
)

// SendHumanInput writes a person's keystrokes, recording when they typed.
func (m *PTYBackend) SendHumanInput(name string, data []byte) error {
	m.mu.Lock()
	if m.humanAt == nil {
		m.humanAt = map[string]time.Time{}
	}
	now := time.Now()
	m.humanAt[name] = now
	if len(m.humanAt) > humanAtKeysMax { // forget sessions nobody has typed in for a while (#3999)
		for k, at := range m.humanAt {
			if now.Sub(at) > humanAtForgetAfter {
				delete(m.humanAt, k)
			}
		}
	}
	m.mu.Unlock()
	return m.sendInput(inputlog.Human(context.Background()), name, data)
}

// HumanInputSince reports whether a person typed in the session's terminal after t.
func (m *PTYBackend) HumanInputSince(name string, t time.Time) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.humanAt[name].After(t)
}

func (m *PTYBackend) SendInput(name string, data []byte) error {
	return m.sendInput(context.Background(), name, data)
}

// sendInput writes data to the session's terminal; ctx says whose input it is (inputlog.Human).
func (m *PTYBackend) sendInput(ctx context.Context, name string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok || !s.isRunning() {
		return fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
	}
	if _, err := s.writeFullCtx(ctx, data); err != nil {
		if !s.isRunning() {
			return fmt.Errorf("%w: session %q ended: %v", ErrTerminalGone, name, err) // the process went between the check and the write
		}
		return err
	}
	return nil
}

// CursorKeysApp reports whether the session's program turned on application cursor-key mode (DECCKM): its arrow
// keys are then ESC O x, not ESC [ x.
func (m *PTYBackend) CursorKeysApp(name string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	return ok && s.cursorKeysApp.Load()
}

// promptSubmitDelay (a field of waits(), seams.go) separates a bracketed paste from the submitting Enter.

// SendLine types text and then submits it with Enter. The Enter is written
// separately after a short delay: agent TUIs treat text that arrives in the same
// read as a trailing newline as a paste, leaving it in the input box unsent.
func (m *PTYBackend) SendLine(name, text string) error {
	return m.SendLineCtx(context.Background(), name, text)
}

// SendLineCtx is SendLine for input sent with ctx: a person's (inputlog.Human) is logged by its
// length only (#60 review N3), and ctx's WithBeforeEnter check is run just before the Enter, as
// tmux's typeToTarget does: when a prompt opened after the text was typed it returns
// tmux.ErrEnterWithheld and the gate holds the Enter (#3431), not letting it answer the prompt.
func (m *PTYBackend) SendLineCtx(ctx context.Context, name, text string) error {
	if text != "" {
		if err := m.sendInput(ctx, name, []byte(text)); err != nil {
			return err
		}
		time.Sleep(lineSubmitDelay)
	}
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld
	}
	return m.sendInput(ctx, name, []byte(EnterKey))
}

// pastesAsText reports whether text for an agent goes in as one paste rather than as typed keys: it has a line break
// (LF or CR, so CRLF too) or is longer than a command typed into a shell may be. tmux pastes the same text
// (typeToTarget), so an agent that treats a paste differently from fast keystrokes (Codex's paste-burst, a menu that
// ignores a paste) sees the same input on both backends.
func pastesAsText(text string) bool {
	return strings.ContainsAny(text, "\r\n") || len(text) > inlineCommandLimit
}

// SendText types text for an agent and submits it with Enter. Text with a line break, or a long one, is pasted: one
// bracketed paste (ESC [ 200 ~ ... ESC [ 201 ~) when the application enabled bracketed paste, as tmux's paste-buffer -p
// does, so the lines are one input and CRLF is one line break; an application that did not is given the lines with CR
// for each break, as tmux's paste-buffer gives them. Short single-line text is typed (SendLineCtx). ctx's
// WithBeforeEnter check runs before the Enter, as for every typed send.
func (m *PTYBackend) SendText(ctx context.Context, name, text string) error {
	if tmux.Automated(ctx) {
		// Coral's own text (the gate): what is typed has no control character in it and no line break, so nothing in a board
		// message or a task title reaches the agent as a key, or runs a shell line by line when the agent dropped back to
		// one. A multi-line text is kept only for a bracketed paste, which SanitizePaste has already made safe.
		m.mu.RLock()
		s, ok := m.sessions[name]
		m.mu.RUnlock()
		if !(ok && s.bracketedPaste.Load() && pastesAsText(text)) {
			var err error
			if text, err = tmux.AutomatedTextChecked(text); err != nil {
				return err // only control characters: no Enter over whatever is in the input
			}
		}
	}
	if !pastesAsText(text) {
		return m.SendLineCtx(ctx, name, text)
	}
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok || !s.isRunning() {
		return fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
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
	body := tmux.SanitizePaste(text) // CRLF and CR are one LF; control characters, ESC included, are dropped
	if tmux.Automated(ctx) && text != "" && strings.TrimSpace(body) == "" {
		return tmux.ErrNothingToSend // only control characters: no empty paste and Enter over whatever is in the input
	}
	if s.bracketedPaste.Load() {
		body = "\x1b[200~" + body + "\x1b[201~"
	} else {
		body = strings.ReplaceAll(body, "\n", EnterKey)
	}
	if n, err := s.writeFullCtx(ctx, []byte(body)); err != nil { // a person's paste is logged by its length (#60 N3)
		if n > 0 {
			return tmux.MarkDeliveryUnknown(fmt.Errorf("paste write failed after %d of %d bytes: %w", n, len(body), err))
		}
		return fmt.Errorf("write text: %w", err) // nothing reached the PTY
	}
	// Input has started: finish regardless of ctx, bounded by the fixed delay.
	time.Sleep(waits().promptSubmitDelay)
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld // a prompt opened: the gate holds the Enter (#3431)
	}
	if n, err := s.writeFull([]byte(EnterKey)); err != nil || n != len(EnterKey) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return tmux.MarkDeliveryUnknown(fmt.Errorf("pasted the text but sending Enter failed: %w", err))
	}
	return nil
}

// lineSubmitDelay separates typed text from the submitting Enter in SendLine.
// Codex on Windows reads an Enter that follows typed text too closely as a
// newline in the input box (paste-burst handling) instead of a submit: with
// 100-150ms the text was left unsent, while 200ms+ submitted every time.
var lineSubmitDelay = 400 * time.Millisecond

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
		return fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
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
	n, err := s.writeFullCtx(ctx, payload) // a person's paste is logged by its length (#60 N3)
	if err != nil {
		if n > 0 {
			return tmux.MarkDeliveryUnknown(fmt.Errorf("paste write failed after %d of %d bytes: %w", n, len(payload), err))
		}
		return fmt.Errorf("write prompt: %w", err) // nothing reached the PTY
	}
	// Input has started: finish regardless of ctx, bounded by the fixed delay.
	time.Sleep(waits().promptSubmitDelay)
	if !tmux.EnterAllowed(ctx) {
		return tmux.ErrEnterWithheld // a prompt opened: the gate holds the Enter (#3457)
	}
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
	if !ok || !s.isRunning() {
		return fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
	}
	return s.resize(cols, rows)
}

// PaneSize reports the size the session's PTY was last given (#3422).
func (m *PTYBackend) PaneSize(name string) (int, int, error) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok || !s.isRunning() {
		return 0, 0, fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
	}
	cols, rows := s.size()
	return int(cols), int(rows), nil
}

// SessionPID is the process a running session was started with: what coral-board's
// process-tree identity is checked against (#3864).
func (m *PTYBackend) SessionPID(_ context.Context, name string) (int, error) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok || !s.isRunning() {
		return 0, fmt.Errorf("%w: session %q not found", ErrTerminalGone, name)
	}
	return s.proc.Pid(), nil
}

// AgentPIDs is the process each running session was started with (#3444).
func (m *PTYBackend) AgentPIDs() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var pids []int
	for _, s := range m.sessions {
		if s.isRunning() {
			if pid := s.proc.Pid(); pid > 0 {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func (m *PTYBackend) Attach(name, subscriberID string) (<-chan []byte, error) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session %q not found: %w", name, ErrTerminalGone)
	}
	return s.subscribe(subscriberID), nil
}

// Lagged implements LagReporter (#4161).
func (m *PTYBackend) Lagged(name, subscriberID string) bool {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	return ok && s.takeLagged(subscriberID)
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

	// Together, not one after another: each waits up to 5 s for its process, so N sessions that ignore SIGTERM held a shutdown (and every
	// test that leaves shells behind) for N times that (#4852).
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		s := s
		safego.Go("ptymanager/close", func() {
			defer wg.Done()
			s.kill()
		})
	}
	wg.Wait()
	return nil
}

// shellPIDReporter is implemented by pseudo-terminals that can name the process
// they started (Unix). ConPTY sessions don't, so on Windows nothing is reported.
type shellPIDReporter interface {
	ShellPID() int
}

// ShellIdle reports whether a session's own shell is idle, with nothing running
// in it: the agent CLI has exited (#3219). ok is false when the session is gone
// or the platform cannot tell.
func (m *PTYBackend) ShellIdle(name string) (idle bool, shell string, ok bool) {
	m.mu.RLock()
	s, found := m.sessions[name]
	m.mu.RUnlock()
	if !found || !s.isRunning() {
		return false, "", false
	}
	sp, can := s.proc.(shellPIDReporter)
	if !can {
		return false, "", false
	}
	idle, shell, err := tmux.ShellIdle(sp.ShellPID())
	if err != nil {
		return false, "", false
	}
	return idle, shell, true
}
