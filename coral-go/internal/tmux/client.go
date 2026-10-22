// Package tmux provides a client for interacting with tmux sessions.
package tmux

import (
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/subgentic/coral/internal/testguard"
)

// Pane represents a tmux pane with its metadata.
type Pane struct {
	PaneTitle   string `json:"pane_title"`
	SessionName string `json:"session_name"`
	Target      string `json:"target"`
	CurrentPath string `json:"current_path"`
	SocketPath  string `json:"-"` // which tmux socket this pane was found on (empty = default)
}

// Client wraps tmux command execution.
type Client struct {
	// TmuxBin is the path to the tmux binary. Defaults to "tmux".
	// Read/write through resolveTmuxBin() so a user-installed tmux is picked
	// up without restarting the server.
	TmuxBin   string
	tmuxBinMu sync.Mutex
	// SocketPath is an explicit tmux socket path (-S flag).
	// If set, all commands use this socket for consistent session visibility
	// across different launch contexts (terminal vs app bundle).
	SocketPath string
	// FallbackToDefault enables checking the default tmux socket (no -S flag)
	// when no sessions are found on the primary socket. This provides backward
	// compatibility with sessions created before the fixed socket was introduced.
	FallbackToDefault bool
	// sessionSockets caches which socket each session was found on.
	// Populated by ListPanes, used by runForTarget to route commands.
	// Protected by socketsMu since multiple goroutines access this map.
	sessionSockets map[string]string
	socketsMu      sync.RWMutex

	// captureTargets caches the pane target CapturePane resolved for a session (#3439): a
	// capture is then one tmux exec (capture-pane) instead of two (list-panes first). Only
	// for a session id, whose tmux session name the target contains; a failed capture drops it.
	captureTargetsMu sync.Mutex
	captureTargets   map[string]string // agentName \x00 agentType \x00 sessionID -> target

	// snap is ONE list-panes of the primary socket, shared for paneStateTTL by every reader: the
	// panes (ListPanes: the board notifier's and the status poller's agent discovery) and each
	// pane's pid and size (#3847: the status pass and the feed). Before #4049 those were two
	// list-panes, and ListPanes ran on every call.
	paneStatesMu  sync.Mutex
	snap          *paneSnapshot
	paneStatesGen uint64 // bumped by every command that changes a pane: a list started before it is not kept
}

// NewClient creates a new tmux Client.
// coralDir is the root data directory (e.g. ~/.coral). The tmux socket is
// placed at <coralDir>/tmux.sock. Pass "" to use CORAL_TMUX_SOCKET env var
// or fall back to ~/.coral/tmux.sock.
func NewClient(coralDir ...string) *Client {
	c := &Client{TmuxBin: "tmux", sessionSockets: make(map[string]string)}

	// Find tmux binary if not on PATH (native app may not have Homebrew paths).
	if p, ok := IsAvailable(); ok {
		c.TmuxBin = p
	}

	if len(coralDir) > 0 && coralDir[0] != "" {
		c.SocketPath = filepath.Join(coralDir[0], "tmux.sock")
	} else if sp := os.Getenv("CORAL_TMUX_SOCKET"); sp != "" {
		c.SocketPath = sp
	} else {
		home, _ := os.UserHomeDir()
		if home != "" {
			c.SocketPath = filepath.Join(home, ".coral", "tmux.sock")
		}
	}
	testguard.CheckDataPath(c.SocketPath)
	c.FallbackToDefault = defaultSocketFallback(c.SocketPath)
	log.Printf("[tmux] NewClient: tmux_bin=%q socket=%q fallback=%v shell=%q coral_tmux_bin_set=%t", logsafe.Path(c.TmuxBin), logsafe.Path(c.SocketPath), c.FallbackToDefault, shellName(), os.Getenv("CORAL_TMUX_BIN") != "") // no PATH value: it names profile folders (#6004 W9)
	return c
}
