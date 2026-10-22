package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
)

// The tag of an agent started on tmux (#6141). Everything such an agent starts carries CORAL_PTY_RUN=<tag> in its environment (the launch line exports it with the agent's other variables), so Kill, sleep and
// Restart can find a helper that has since detached from the agent (a daemon, reparented to PID 1) and end it with the agent. It is random, 128 bits, kept in the session's row (live_sessions.run_tag) and handed
// on when the session is restarted, resumed or woken, so a Kill also ends what an earlier run of the same session left behind. The built-in terminal has a tag of its own per terminal and is not given one here.

// pendingRunTags holds, by session id, the tag chosen when a launch command is built until the launch is noted (the new row may not exist yet).
var pendingRunTags sync.Map

// runTagFor is the tag for the agent about to start as newID, "" when it does not start on tmux: the tag already recorded for newID or, failing that, for the session it continues (prevIDs, in order), else
// a new random one. The caller puts it in the launch parameters; noteLaunchedRunTag records it once the row exists.
func (h *SessionsHandler) runTagFor(ctx context.Context, newID string, prevIDs ...string) string {
	if terminalKind(h.backend) != "tmux" || h.ss == nil || newID == "" {
		return ""
	}
	tag := h.ss.RunTagOf(ctx, newID)
	for _, prev := range prevIDs {
		if tag == "" && prev != "" {
			tag = h.ss.RunTagOf(ctx, prev)
		}
	}
	if tag == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			slog.Warn("could not make a tag for a tmux agent: its detached helpers will not be found at Kill", "error", err)
			return ""
		}
		tag = hex.EncodeToString(b)
	}
	pendingRunTags.Store(newID, tag)
	return tag
}

// noteLaunchedRunTag records the tag the agent just started with (hook_dir's twin: at every launch, restart, resume and wake).
func (h *SessionsHandler) noteLaunchedRunTag(ctx context.Context, sessionID string) {
	v, ok := pendingRunTags.LoadAndDelete(sessionID)
	if !ok || h.ss == nil {
		return
	}
	if err := h.ss.SetRunTagIfEmpty(ctx, sessionID, v.(string)); err != nil {
		slog.Warn("could not record a tmux agent's run tag", "session_id", sessionID, "error", err)
	}
}
