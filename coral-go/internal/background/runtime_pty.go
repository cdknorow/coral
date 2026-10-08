package background

import (
	"context"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// PTYRuntime implements AgentRuntime using native PTY sessions.
// Used on Windows (ConPTY) or macOS App Store (no tmux available).
type PTYRuntime struct {
	backend *ptymanager.PTYBackend
}

// NewPTYRuntime creates a PTYRuntime wrapping the given PTY backend.
func NewPTYRuntime(backend *ptymanager.PTYBackend) *PTYRuntime {
	return &PTYRuntime{backend: backend}
}

func (r *PTYRuntime) SpawnAgent(ctx context.Context, name, workDir, logFile, command string) error {
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

func (r *PTYRuntime) SendInput(ctx context.Context, name, text string) error {
	// Type the text, then submit with the platform's Enter key.
	return r.backend.SendLine(name, text)
}

func (r *PTYRuntime) KillAgent(ctx context.Context, name string) error {
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
