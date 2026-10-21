package background

import (
	"context"
	"time"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
)

// PTYRuntime implements AgentRuntime using native PTY sessions.
// Used on Windows (ConPTY) or macOS App Store (no tmux available).
type PTYRuntime struct {
	backend *ptymanager.PTYBackend
}

// HumanInputSince implements ptymanager.HumanInputReader: whether a person typed in
// the agent's terminal after t (the gate's PTY rule, #3482).
func (r *PTYRuntime) HumanInputSince(name, sessionID string, t time.Time) bool {
	if sessionID != "" { // the agent's own terminal (#3704)
		for _, s := range r.backend.ListSessions() {
			if s.SessionID == sessionID {
				return r.backend.HumanInputSince(naming.SessionName(s.AgentType, s.SessionID), t)
			}
		}
		return false
	}
	return r.backend.HumanInputSince(name, t)
}

// NewPTYRuntime creates a PTYRuntime wrapping the given PTY backend.
func NewPTYRuntime(backend *ptymanager.PTYBackend) *PTYRuntime {
	return &PTYRuntime{backend: backend}
}

func (r *PTYRuntime) SpawnAgent(ctx context.Context, name, workDir, logFile, command string) error {
	ptymanager.DropHeld(name, "spawned") // nothing held for the old process is typed into a new one (#3431)
	// Extract agent type and session ID from name format "{type}-{uuid}"
	agentType, sessionID := parseSessionName(name)
	if agentType == "" {
		agentType = "agent"
	}
	if sessionID == "" {
		sessionID = name
	}

	return r.backend.Spawn(name, agentType, workDir, sessionID, command, 200, 50)
}

func (r *PTYRuntime) SendInput(ctx context.Context, o terminalinput.Origin, name, text string) error {
	// Type the text, then submit with the platform's Enter key (unless the gate's check withholds it).
	return r.backend.SendText(ctx, o, name, text)
}

// SendPrompt implements ptymanager.PromptSender: automated text as one bracketed
// paste and its Enter, while the agent has bracketed paste on (#3457).
func (r *PTYRuntime) SendPrompt(ctx context.Context, o terminalinput.Origin, name, text, _, _ string) error {
	return r.backend.SendPrompt(ctx, o, name, text)
}

// SendEnter implements ptymanager.EnterSender (#3431, #3457).
func (r *PTYRuntime) SendEnter(_ context.Context, name, _, _ string) error {
	return r.backend.SendInput(terminalinput.Internal, name, []byte("\r"))
}

func (r *PTYRuntime) KillAgent(ctx context.Context, name string) error {
	ptymanager.DropHeld(name, "killed") // nothing held for the old process is typed into a new one (#3431)
	return r.backend.Kill(name)
}

func (r *PTYRuntime) IsAlive(ctx context.Context, name string) bool {
	return r.backend.IsRunning(name)
}

func (r *PTYRuntime) ListAgents(ctx context.Context) ([]AgentInfo, error) {
	sessions := r.backend.ListSessions()
	agents := make([]AgentInfo, 0, len(sessions))
	for _, s := range sessions {
		agents = append(agents, AgentInfo{
			AgentName:        s.AgentName,
			AgentType:        s.AgentType,
			SessionID:        s.SessionID,
			WorkingDirectory: s.WorkingDir,
		})
	}
	return agents, nil
}

// Verify interface compliance at compile time.
var _ AgentRuntime = (*PTYRuntime)(nil)
var _ AgentRuntime = (*TmuxRuntime)(nil)

// FormatSessionName builds a session name from agent type and session ID.
func FormatSessionName(agentType, sessionID string) string {
	return naming.SessionName(agentType, sessionID)
}

// AgentShellIdle reports whether an agent's terminal is back at its idle shell,
// for ptymanager.AgentExited (#3219).
func (r *PTYRuntime) AgentShellIdle(_ context.Context, name, _, _ string) (bool, string, bool) {
	return r.backend.ShellIdle(name)
}

// AgentScreen implements ptymanager.ScreenReader: the session's output rendered as the
// screen (as the HTTP senders read it), read by the automated-input gate before typing
// (#3431).
func (r *PTYRuntime) AgentScreen(_ context.Context, name, _, _ string) (string, error) {
	return r.backend.RenderedScreenText(name) // an empty render stays empty: the gate holds rather than guessing
}

// AgentScreenStyled implements ptymanager.StyledScreenReader: AgentScreen with faint cells marked.
func (r *PTYRuntime) AgentScreenStyled(_ context.Context, name, _, _ string) (string, error) {
	return r.backend.RenderedScreenStyledText(name)
}

// HasRenderedScreen implements ptymanager.RenderedScreen: AgentScreen is the rendered screen.
func (r *PTYRuntime) HasRenderedScreen() bool { return true }
