// Package naming provides canonical constructors for agent identity strings.
// These are the single source of truth — use them everywhere instead of
// ad-hoc fmt.Sprintf calls.
package naming

import (
	"fmt"
	"github.com/cdknorow/coral/internal/safefs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// SessionName returns the canonical tmux/pty session name for an agent.
// Format: "{agentType}-{sessionID}", e.g. "claude-dc6d10f4-2f20-...".
func SessionName(agentType, sessionID string) string {
	return fmt.Sprintf("%s-%s", agentType, sessionID)
}

// SessionIDFromName extracts the session UUID from a tmux/pty session name.
// Inverse of SessionName: "claude-dc6d10f4-2f20-..." → "dc6d10f4-2f20-...".
// Returns the original string unchanged if no prefix is found.
func SessionIDFromName(sessionName string) string {
	if idx := strings.Index(sessionName, "-"); idx >= 0 {
		return sessionName[idx+1:]
	}
	return sessionName
}

// SubscriberID returns the stable board identity for an agent.
// Uses the display name (role) if set, otherwise falls back to the agent type.
// Examples: "Orchestrator", "Backend Dev", "claude".
func SubscriberID(displayName, agentType string) string {
	if displayName != "" {
		return displayName
	}
	return agentType
}

var (
	defaultLogDirMu sync.RWMutex
	defaultLogDir   string
)

// SetDefaultLogDir sets where agent pipe logs go for code that has no config at hand
// (the terminal backends, the launcher, the idle detector). Startup sets it from
// config.LogDir; empty means the system temp dir.
func SetDefaultLogDir(dir string) {
	defaultLogDirMu.Lock()
	defaultLogDir = dir
	defaultLogDirMu.Unlock()
}

// DefaultLogDir is the directory agent pipe logs are written to: the one startup set,
// otherwise the system temp dir.
func DefaultLogDir() string {
	defaultLogDirMu.RLock()
	dir := defaultLogDir
	defaultLogDirMu.RUnlock()
	if dir == "" {
		return os.TempDir()
	}
	return dir
}

// LogFile returns the canonical log file path for an agent session.
// Format: "{logDir}/{agentType}_coral_{sessionID}.log".
//
// Both parts come from callers that may pass a request's or a scheduled job's values, so the
// path is built only from an agent type of [A-Za-z0-9_] and a session id that is one safe path
// element (safefs.IsSafeID); anything else gives "" (#3602 W14, W15): no file is opened, made
// or removed outside logDir.
func LogFile(logDir, agentType, sessionID string) string {
	if !wordRe.MatchString(agentType) || !safefs.IsSafeID(sessionID) {
		return ""
	}
	return filepath.Join(logDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))
}

var wordRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
