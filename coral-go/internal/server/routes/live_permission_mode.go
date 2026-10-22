package routes

// The permission mode a Claude session is in NOW, as its CLI reports it: every Claude hook payload carries permission_mode, which follows Shift+Tab. The hook forwards it with its event, the
// server keeps the latest per session, and the live rows carry it as live_permission_mode so the dashboard's Mode button reads the CLI's own signal before it reads the screen. Not a secret: a mode
// word, one of the six the CLI has. An event only moves it when it comes from that session's own hooks (the token), for a Claude agent, with one of the six words.

// liveModeWords are the permission modes Claude Code names (its next-mode table, 2.1.308): anything else is dropped, never stored.
var liveModeWords = map[string]bool{"default": true, "acceptEdits": true, "plan": true, "auto": true, "bypassPermissions": true, "dontAsk": true}

// noteLiveMode keeps the mode a Claude session's hook reported. A word that is not one of the six, or a session of another agent type, changes nothing.
func (h *SessionsHandler) noteLiveMode(sessionID, agentType, mode string) {
	if sessionID == "" || agentType != "claude" || !liveModeWords[mode] {
		return
	}
	h.liveModes.Store(sessionID, mode)
}

// liveMode is the session's last reported mode for a live row: the word, or nil (absent for other agents and before the first event).
func (h *SessionsHandler) liveMode(sessionID string) any {
	if v, ok := h.liveModes.Load(sessionID); ok {
		return v
	}
	return nil
}
