package ptymanager

import (
	"context"
	"github.com/subgentic/coral/internal/terminalinput"
	"sync/atomic"
)

// SessionInfo holds metadata about a running terminal session.
type SessionInfo struct {
	AgentName  string `json:"agent_name"`
	AgentType  string `json:"agent_type"`
	SessionID  string `json:"session_id"`
	WorkingDir string `json:"working_dir"`
	Running    bool   `json:"running"`
}

var replayBytes atomic.Int64

func init() {
	replayBytes.Store(256 * 1024) // 256 KiB default
}

// ReplayBytes returns the current replay buffer size limit.
func ReplayBytes() int {
	return int(replayBytes.Load())
}

// SetReplayBytes updates the replay buffer size limit at runtime.
func SetReplayBytes(n int) {
	if n <= 0 {
		n = 256 * 1024
	}
	replayBytes.Store(int64(n))
}

// PaneSizer is implemented by backends that can report a session's current terminal size
// (#3422). The terminal socket tells every viewer the pane's real size, so a viewer that does
// not set it (view-only, not the owner, a phone) renders at that size instead of its own.
type PaneSizer interface {
	PaneSize(name string) (cols, rows int, err error)
}

// FreshPaneSizer is implemented by backends whose PaneSize may be a moment old: PaneSizeFresh asks the terminal itself, for a viewer's first report and the answer to its own resize (#6706).
type FreshPaneSizer interface {
	PaneSizeFresh(name string) (cols, rows int, err error)
}

// OutputStamper is implemented by backends that can tell cheaply whether a session's terminal has
// written anything since an earlier stamp (#3439). The poll endpoint then reuses its last capture
// instead of running capture-pane while nothing was written. ok is false when it cannot tell.
type OutputStamper interface {
	OutputStamp(agentType, sessionID string) (stamp string, ok bool)
}

// AgentProcesses is implemented by backends that can name the processes their agents run in
// (#3444): the roots of the process trees whose CPU and memory the machine health sums up.
type AgentProcesses interface {
	AgentPIDs() []int
}

// TerminalBackend abstracts terminal session management.
// Both PTY and tmux backends implement this interface.
type TerminalBackend interface {
	// Spawn starts a new terminal session running the given command.
	Spawn(name, agentType, workDir, sessionID, command string, cols, rows uint16) error

	// Kill terminates a session and its child processes.
	Kill(name string) error

	// Restart kills and re-spawns a session with a new command.
	Restart(name, command string) error

	// SendInput writes raw bytes to the session's terminal input.
	SendInput(o terminalinput.Origin, name string, data []byte) error

	// Resize changes the terminal dimensions.
	Resize(name string, cols, rows uint16) error

	// Attach registers a subscriber for live terminal output.
	// Returns a channel that receives raw PTY output bytes.
	// Never returns a nil channel for a live session.
	Attach(name, subscriberID string) (<-chan []byte, error)

	// Unsubscribe removes a subscriber.
	Unsubscribe(name, subscriberID string)

	// Replay returns recent output bytes for reconnect seed.
	// Returns up to ReplayBytes() of recent output.
	Replay(name string) ([]byte, error)

	// ListSessions returns info about all active sessions.
	ListSessions() []SessionInfo

	// IsRunning returns true if the session's process is still running.
	IsRunning(name string) bool

	// LogPath returns the log file path for a session.
	LogPath(name string) string

	// Close shuts down all sessions and cleans up resources.
	Close() error
}

// SessionPIDProvider is implemented by terminal backends that can identify the
// root process for a spawned session. It is optional so backends without a
// stable process identifier can still implement TerminalBackend.
type SessionPIDProvider interface {
	SessionPID(ctx context.Context, name string) (int, error)
}
