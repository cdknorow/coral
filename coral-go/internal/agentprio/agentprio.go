// Package agentprio runs agents at a lower OS priority than the Coral server
// (#3437), so a machine whose agents burn every CPU still answers the dashboard
// and Coral Hub quickly. Agents' processes (a tmux pane and everything it
// starts, a PTY session's child, a workflow step) are reniced right after they
// start; their children inherit it. The server itself, and the tmux server it
// talks to, keep normal priority.
//
// The level is the agent_priority setting ("low", the default, or "normal"),
// applied live to agents started afterwards. CORAL_AGENT_PRIORITY or
// --agent-priority override it. An unprivileged process can lower priority but
// not raise it back, so "normal" applies to agents started after the change;
// running ones keep theirs until restarted.
package agentprio

import (
	"log/slog"
	"strings"
	"sync"
)

const (
	// SettingKey is the setting's name.
	SettingKey = "agent_priority"
	// EnvVar overrides the setting.
	EnvVar = "CORAL_AGENT_PRIORITY"
	// Low is the default: agents at nice +10 (BELOW_NORMAL on Windows).
	Low = "low"
	// Normal leaves agents at the server's priority.
	Normal = "normal"
	// Nice is the niceness low-priority agents get.
	Nice = 10
)

var state struct {
	sync.RWMutex
	setting string // from the settings store
	forced  string // from the flag or env, wins over the setting
}

// Parse normalises a level; ok is false for anything but low or normal.
func Parse(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case Low:
		return Low, true
	case Normal:
		return Normal, true
	}
	return "", false
}

// Setup installs the flag or env override ("" for none). A bad value is
// logged and ignored.
func Setup(forced string) {
	if forced == "" {
		return
	}
	lvl, ok := Parse(forced)
	if !ok {
		slog.Warn("agent priority: unknown level, keeping the setting", "value", forced, "want", "low or normal")
		return
	}
	state.Lock()
	state.forced = lvl
	state.Unlock()
}

// SetSetting applies the agent_priority setting; it takes effect for agents
// started afterwards. "" or an unknown value means the default (low).
func SetSetting(v string) {
	lvl, ok := Parse(v)
	if !ok {
		lvl = Low
	}
	state.Lock()
	state.setting = lvl
	state.Unlock()
}

// Level is the level in effect.
func Level() string {
	state.RLock()
	defer state.RUnlock()
	if state.forced != "" {
		return state.forced
	}
	if state.setting != "" {
		return state.setting
	}
	return Low
}

// Forced reports whether the flag or env decides the level.
func Forced() bool {
	state.RLock()
	defer state.RUnlock()
	return state.forced != ""
}

// Apply lowers the priority of process pid if agents run low. Best effort: an
// error is logged once per kind and otherwise ignored.
func Apply(pid int) {
	if pid <= 0 || Level() != Low {
		return
	}
	if err := lower(pid); err != nil {
		logOnce("process", err)
	}
}

// ApplyGroup lowers the priority of every process in process group pgid (a
// workflow step started with Setpgid) if agents run low.
func ApplyGroup(pgid int) {
	if pgid <= 0 || Level() != Low {
		return
	}
	if err := lowerGroup(pgid); err != nil {
		logOnce("group", err)
	}
}

var logged sync.Map

func logOnce(kind string, err error) {
	if _, done := logged.LoadOrStore(kind, true); !done {
		slog.Warn("agent priority: could not lower an agent's priority", "kind", kind, "error", err)
	}
}

// proc is a process from a snapshot, with the parent it had then (0 when the
// snapshot didn't give one).
type proc struct{ pid, ppid int }

// ApplyTree lowers the priority of pid and every process descended from it, if
// agents run low, and returns how many it lowered: for processes started before
// the level applied (an older Coral's tmux panes, or a child an agent pane
// forked before its own renice). The tree is a snapshot, so each process's
// parent is read again just before it is lowered and it is skipped if that
// changed (it exited and its pid was reused). Unix only.
func ApplyTree(pid int) int {
	if pid <= 0 || Level() != Low {
		return 0
	}
	return lowerTree(descendants(pid))
}

// lowerTree lowers each process of a snapshot whose parent is unchanged.
func lowerTree(procs []proc) int {
	n := 0
	for _, p := range procs {
		if p.ppid != 0 {
			if now, ok := parentOf(p.pid); !ok || now != p.ppid {
				continue
			}
		}
		if err := lower(p.pid); err != nil {
			logOnce("tree", err)
			continue
		}
		n++
	}
	return n
}
