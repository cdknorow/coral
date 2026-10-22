package routes

import (
	"log/slog"
	"net/http"
	"runtime"
	"strings"

	"github.com/subgentic/coral/internal/backendchoice"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/tmux"
)

// Settings > Terminal backend (#3634): tmux keeps agents running across Coral restarts but
// shares a socket any agent can use against another; pty isolates agents but they end when
// Coral restarts. One Coral runs one backend, chosen at startup (--backend wins), so a change
// applies at the next start. Writing it, and ending agents left in Coral's private tmux server
// after a switch to pty, are the Operator's: this computer's dashboard, as a person.

// TerminalBackendSetting holds "tmux" or "pty", read only at startup. It is written here only;
// the generic settings PUT refuses it.
const TerminalBackendSetting = "terminal_backend"

// backendChoice reports whether this system can run either backend: not Windows (pty only)
// and tmux installed (or bundled).
func backendChoice() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	_, ok := tmux.IsAvailable()
	return ok
}

// refusePersonSystemSetting is refuseSystemSetting plus a person: an agent's token never
// passes, whatever browser headers it sends.
func (h *CloudHandler) refusePersonSystemSetting(w http.ResponseWriter, r *http.Request) bool {
	if h.refuseSystemSetting(w, r) {
		return true
	}
	if !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can change the terminal backend")
		return true
	}
	return false
}

// TerminalBackend: GET/PUT /api/system/terminal-backend. PUT {"backend": "tmux"|"pty"} saves
// the setting for the next start. The answer: {saved, running, forced, choice, restart_required,
// can_restart, running_agents, leftover_tmux_agents, next, default_notice}.
func (h *CloudHandler) TerminalBackend(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		if h.refusePersonSystemSetting(w, r) {
			return
		}
		if !requireJSON(w, r) {
			return
		}
		var body struct {
			Backend string `json:"backend"`
		}
		if err := decodeJSON(r, &body); err != nil || (body.Backend != "tmux" && body.Backend != "pty") {
			errBadRequest(w, "backend must be exactly \"tmux\" or \"pty\"")
			return
		}
		if !backendChoice() && body.Backend != "pty" {
			errBadRequest(w, "this system runs pty only (no tmux here)")
			return
		}
		if err := h.ss.SetSetting(r.Context(), TerminalBackendSetting, body.Backend); err != nil {
			errInternalServer(w, "could not save the terminal backend")
			return
		}
		slog.Info("[system] terminal backend saved; it applies at the next start", "backend", body.Backend, "running", h.BackendRunning)
	} else if h.refuseSystemSetting(w, r) {
		return
	}
	s, _ := h.ss.GetSettings(r.Context())
	saved := setreg.Enum(s, TerminalBackendSetting)
	if saved != "tmux" && saved != "pty" {
		saved = ""
	}
	// What the next start runs: the same rules the start uses (backendchoice), so the dashboard never asks for a restart the start would not make.
	next, _, _ := backendchoice.Resolve("", saved, runtime.GOOS, func() int {
		return len(tmux.PrivateAgentSessions(r.Context(), tmux.PrivateSocketPath(h.dataDir)))
	})
	if next == "tmux" && !backendChoice() {
		next = "pty"
	}
	if h.BackendForced {
		next = h.BackendRunning
	}
	running := 0
	if live, err := h.ss.GetAllLiveSessions(r.Context()); err == nil {
		for _, ls := range live {
			if ls.IsSleeping == 0 {
				running++
			}
		}
	}
	leftover := 0
	if h.BackendRunning == "pty" {
		leftover = len(tmux.PrivateAgentSessions(r.Context(), tmux.PrivateSocketPath(h.dataDir)))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": saved, "running": h.BackendRunning, "forced": h.BackendForced, "choice": backendChoice(),
		"next": next, "restart_required": next != h.BackendRunning, "can_restart": h.Restart != nil,
		"running_agents": running, "leftover_tmux_agents": leftover,
		// The one line "Agents now stop when you quit Coral" (#6064): owed to an install that ran on the old tmux default, until it is dismissed, and not once the person has made a choice.
		"default_notice": setreg.Raw(s, setreg.TerminalDefaultNotice) == "pending" && saved == "" && h.BackendRunning == "pty",
	})
}

// EndLeftoverTmuxAgents: POST /api/system/terminal-backend/end-leftover. Under pty, ends the
// Coral agents still running in Coral's private tmux server (<data dir>/tmux.sock), and only
// those ({type}-{uuid} sessions): never tmux's default socket or the operator's own tmux.
func (h *CloudHandler) EndLeftoverTmuxAgents(w http.ResponseWriter, r *http.Request) {
	if h.refusePersonSystemSetting(w, r) {
		return
	}
	if h.BackendRunning != "pty" {
		writeError(w, http.StatusConflict, "not_pty", "Coral runs on tmux: its agents are reachable, nothing is left over")
		return
	}
	ended := tmux.EndPrivateAgentSessions(r.Context(), tmux.PrivateSocketPath(h.dataDir))
	slog.Info("[system] ended agents left in Coral's private tmux server", "count", len(ended))
	writeJSON(w, http.StatusOK, map[string]any{"ended": len(ended), "left": len(tmux.PrivateAgentSessions(r.Context(), tmux.PrivateSocketPath(h.dataDir)))})
}

// isTerminalBackendKey reports a settings key only TerminalBackend may write.
func isTerminalBackendKey(k string) bool {
	return strings.EqualFold(strings.TrimSpace(k), TerminalBackendSetting)
}

// DismissDefaultNotice: POST /api/system/terminal-backend/notice-seen. The person dismissed the line "Agents now stop when you quit Coral": it is never shown again (#6064). The flag is in
// the server's settings, so it holds across browsers and restarts.
func (h *CloudHandler) DismissDefaultNotice(w http.ResponseWriter, r *http.Request) {
	if h.refusePersonSystemSetting(w, r) {
		return
	}
	if err := h.ss.SetSetting(r.Context(), setreg.TerminalDefaultNotice, "seen"); err != nil {
		errInternalServer(w, "could not record that the notice was dismissed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
