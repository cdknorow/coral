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

// runTagFor is the tag for the agent about to start as newID, "" when it does not start on tmux: the tag already recorded for newID (a wake) or, failing that, the one of the session it replaces (replacedID: a
// restart or resume of that agent) or of the conversation it continues (continuedID), else a new random one. A tag is handed on only while NO OTHER live session holds it: one Kill must never end another live
// agent's helpers, so a tag that a live agent other than the one being replaced still carries is not shared, and a fresh one is made. The caller puts it in the launch parameters; noteLaunchedRunTag records it once
// the row exists.
func (h *SessionsHandler) runTagFor(ctx context.Context, newID, replacedID, continuedID string) string {
	if terminalKind(h.backend) != "tmux" || h.ss == nil || newID == "" {
		return ""
	}
	inheritable := func(from string) string {
		if from == "" {
			return ""
		}
		tag := h.ss.RunTagOf(ctx, from)
		if tag == "" || h.ss.LiveRunTagHolders(ctx, tag, newID, replacedID) > 0 {
			return ""
		}
		return tag
	}
	tag := h.ss.RunTagOf(ctx, newID)
	if tag == "" {
		tag = inheritable(replacedID)
	}
	if tag == "" {
		tag = inheritable(continuedID)
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
