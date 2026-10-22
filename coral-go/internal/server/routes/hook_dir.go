package routes

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/agent"
)

// An agent's hooks are the coral-hook-* programs on the PATH it was started with (#6089): Coral puts its tools folder (the app bundle, or the folder next to this build) first. If that
// folder is later gone (the app replaced or moved while the agent kept running), every hook fails with "command not found": the agent sends no event, so Coral cannot see a prompt taken
// by the agent's own hook. The folder is recorded per session at every start, and checked here.

// noteLaunchedHookDir records the tools folder the agent just started with.
func (h *SessionsHandler) noteLaunchedHookDir(ctx context.Context, sessionID string) {
	if sessionID == "" || h.ss == nil {
		return
	}
	// Only a folder that holds hook programs is recorded (#6089 review): a server whose tools folder has coral-board and no hooks ("this build (coral-board only)") started its agents with hooks found
	// elsewhere on their PATH, so "gone" must mean "had hooks here and lost them", never "had none here".
	dir := agent.CoralToolsDir()
	if !dirHasHooks(dir) {
		dir = ""
	}
	if err := h.ss.SetHookDir(ctx, sessionID, dir); err != nil {
		slog.Warn("could not record an agent's tools folder", "session_id", sessionID, "error", err)
	}
}

// dirHasHooks reports whether dir holds at least one coral-hook program.
func dirHasHooks(dir string) bool {
	if dir == "" {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(dir, "coral-hook-*"))
	return err == nil && len(matches) > 0
}

// hookDirGone reports whether the tools folder an agent was started with can no longer run its hooks: the folder is gone, or no coral-hook program is left in it. A folder that cannot be
// read for another reason (permissions) is not "gone": nothing is said that Coral does not know.
func hookDirGone(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(dir); err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "coral-hook-*"))
	return err == nil && len(matches) == 0
}

// hooksUnreachable reports whether this agent's hook programs are gone from where it was started to find them.
func (h *SessionsHandler) hooksUnreachable(ctx context.Context, sessionID string) bool {
	if sessionID == "" || h.ss == nil {
		return false
	}
	dirs, err := h.ss.HookDirs(ctx)
	if err != nil {
		return false
	}
	return hookDirGone(dirs[sessionID])
}
