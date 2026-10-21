package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tracking"
)

// ── Commands ────────────────────────────────────────────────────────────

// Send sends a command to a live tmux session.
// POST /api/sessions/live/{name}/send
func (h *SessionsHandler) Send(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Command   string `json:"command"`
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Command == "" {
		errBadRequest(w, "No command provided")
		return
	}
	// Input always names its exact target (session_id + agent_type). A bare name
	// would reach whatever tmux pane has it, sleeping or archived agents included.
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		if status == http.StatusServiceUnavailable {
			writeJSON(w, status, map[string]any{"error": message + " No input was sent.", "code": "agent_metadata_unavailable", "not_sent": true})
		} else {
			writeExactTargetError(w, status, message)
		}
		return
	}

	// Only the automated path sends through this closure (a person's send is deliverHumanSend, as a person's): Coral's text.
	send := func(ctx context.Context) error {
		return h.terminal.SendInput(ctx, terminalinput.Coral, name, body.Command, body.AgentType, body.SessionID)
	}
	// A person (the dashboard, Coral Hub at Interact, the operator's signed CLI)
	// types directly. Anything else (an agent's tool, a script, an API key) is
	// automation: it goes through the automated-input gate and is held while the
	// agent waits on a prompt, so it can't answer one (#3431).
	if !humanInputCaller(r) {
		if held := h.sendAutomatedHTTP(w, r, name, body.AgentType, body.SessionID, body.Command, "/send", send); held {
			return
		}
	} else if err := h.deliverHumanSend(r.Context(), name, body.AgentType, body.SessionID, body.Command); err != nil {
		// A person's message is submitted, or refused or reported as not submitted: never "sent" for text left
		// in the agent's input box (#4587).
		if status, out, ok := humanSendError(err); ok {
			writeJSON(w, status, out)
			return
		}
		writeTerminalError(w, err)
		return
	}
	// The terminal accepted the input over HTTP: the narrowly labelled
	// first_prompt_submitted milestone. It says nothing about what the agent did.
	tracking.TrackOnce(tracking.EventFirstPromptSubmitted, map[string]string{"source": "http_send"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command": body.Command})
}

// Keys sends raw tmux key names to a session.
// POST /api/sessions/live/{name}/keys
func (h *SessionsHandler) Keys(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Keys      []string `json:"keys"`
		AgentType string   `json:"agent_type"`
		SessionID string   `json:"session_id"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Keys) == 0 {
		errBadRequest(w, "keys must be a non-empty list")
		return
	}
	if len(body.Keys) > ptymanager.MaxKeysPerRequest {
		errBadRequest(w, fmt.Sprintf("at most %d keys in one request", ptymanager.MaxKeysPerRequest))
		return
	}
	// As for Send: keys always name their exact target.
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}
	// A terminal that can't send a name it doesn't know (the PTY's) refuses the request before anything is typed.
	if kv, ok := h.terminal.(ptymanager.KeyValidator); ok {
		if err := kv.ValidateKeys(body.Keys); err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}

	// The origin of the keys: a person's (the dashboard's) are Unclassified as before, anything else goes through the gate as Coral's.
	send := func(ctx context.Context, o terminalinput.Origin) error {
		return h.terminal.SendRawInput(ctx, o, name, body.Keys, body.AgentType, body.SessionID)
	}
	// As for Send: keys from anything but a person go through the gate (#3431).
	if !humanInputCaller(r) {
		if held := h.sendAutomatedHTTP(w, r, name, body.AgentType, body.SessionID, strings.Join(body.Keys, " "), "/keys", func(ctx context.Context) error { return send(ctx, terminalinput.Coral) }); held {
			return
		}
	} else if err := send(r.Context(), terminalinput.Unclassified); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": body.Keys})
}

// Resize resizes the tmux pane width.
// POST /api/sessions/live/{name}/resize
func (h *SessionsHandler) Resize(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Columns   int    `json:"columns"`
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Columns < minTerminalCols || body.Columns > maxTerminalCols {
		errBadRequest(w, fmt.Sprintf("columns must be from %d to %d", minTerminalCols, maxTerminalCols))
		return
	}
	// As for Send: a resize always names its exact target (#3211).
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}

	if err := h.terminal.ResizeSession(r.Context(), name, body.Columns, body.AgentType, body.SessionID); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "columns": body.Columns})
}
