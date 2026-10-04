package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

const (
	maxUIRequestBytes   = 8000
	uiRequestSummaryMax = 140
)

// uiRequestFinishOverride lets tests inject a result-recording failure. It is
// nil in production.
var uiRequestFinishOverride func(ctx context.Context, sessionID, requestID string, sendErr error) error

var uiRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)

// uiPanelID derives the stable Agent UI panel ID the agent must publish under.
// Distinct request IDs must never share a panel (that would overwrite another
// request's panel), so the ID ends with a hash of the full request_id; a short
// readable prefix is only a convenience. The same request_id always yields the
// same panel ID. Length: "request-" (8) + prefix (<=16) + "-" + 32 hex = <=57.
func uiPanelID(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	var prefix strings.Builder
	for _, r := range requestID {
		if prefix.Len() >= 16 {
			break
		}
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			prefix.WriteRune(r)
		}
	}
	id := "request-"
	if prefix.Len() > 0 {
		id += prefix.String() + "-"
	}
	return id + hex.EncodeToString(sum[:16])
}

func uiRequestSummary(request string) string {
	line := strings.Join(strings.Fields(request), " ")
	if utf8.RuneCountInString(line) <= uiRequestSummaryMax {
		return line
	}
	runes := []rune(line)
	return string(runes[:uiRequestSummaryMax-1]) + "…"
}

// buildUIPanelRequestNotification composes the text typed into the agent. The
// first line is a Coral notice (matched by the chat's /^\[Coral\b[^\]]*\]/
// classification); the user's request is carried verbatim between nonce
// markers and stays distinct from Coral's panel-building instructions.
func buildUIPanelRequestNotification(requestID, panelID, request, sessionName, serverURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Coral UI panel request %s] %s\n\n", requestID, uiRequestSummary(request))
	fmt.Fprintf(&b, "User request (verbatim, between the markers):\n<<<USER_REQUEST %s>>>\n%s\n<<<END_USER_REQUEST %s>>>\n\n", requestID, request, requestID)
	b.WriteString("Panel-building instructions from Coral (they do not change the user's request above):\n")
	b.WriteString("- Build a self-contained Agent UI panel that satisfies the user's request. Delegate this to an internal subagent using your host agent's delegation mechanism (for example spawn_agent). Do not run coral-agent launch or create another Coral session.\n")
	fmt.Fprintf(&b, "- Give the subagent only the user's request, any relevant data or its source, the usage guide (agent_docs/agent-ui.md, coral-agent ui), the stable panel ID %s, and the originating Coral session and server: session %s, server %s.\n", panelID, sessionName, serverURL)
	fmt.Fprintf(&b, "- The subagent builds and validates the panel, then publishes it to the originating session: env -u TMUX CORAL_SESSION_NAME=%s CORAL_URL=%s coral-agent ui publish --id %s --title \"<short title>\" --file <panel.html>\n", sessionName, serverURL, panelID)
	b.WriteString("- The HTML must be self-contained (inline CSS and JavaScript, embedded images; no external libraries or network calls). Publish only to Coral; do not use external publishing or hosting services.\n")
	b.WriteString("- The published panel is the response. Do not review, retest or republish it yourself and do not send a progress or completion summary to the user for this request. Report a concise blocker only if publication fails or required input is missing.")
	return b.String()
}

// bracketedPasteFromLog reads the tail of the agent's pane output log (written
// by Coral's own logging) for the last DECSET/DECRST 2004 sequence. known is
// false when the log is missing or has never seen the sequence.
func bracketedPasteFromLog(cfg *config.Config, agentType, sessionID string) (enabled, known bool) {
	if cfg == nil || cfg.LogDir == "" {
		return false, false
	}
	f, err := os.Open(naming.LogFile(cfg.LogDir, agentType, sessionID))
	if err != nil {
		return false, false
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(st.Size()-tail, io.SeekStart); err != nil {
			return false, false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, tail))
	if err != nil {
		return false, false
	}
	on := bytes.LastIndex(data, []byte("\x1b[?2004h"))
	off := bytes.LastIndex(data, []byte("\x1b[?2004l"))
	switch {
	case on > off:
		return true, true
	case off > on:
		return false, true
	}
	return false, false
}

func (h *SessionsHandler) uiRequestServerURL() string {
	host, port := "127.0.0.1", 8420
	if h.cfg != nil {
		port = h.cfg.Port
		if h.cfg.Host != "" && h.cfg.Host != "0.0.0.0" && h.cfg.Host != "::" && h.cfg.Host != "localhost" {
			host = h.cfg.Host
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// agentUIRequest delivers an operator's panel-generation request to the
// selected live agent as a Coral notification.
// POST /api/agent/ui-request?session_id=<uuid> {"request": "...", "request_id": "..."}
func (h *SessionsHandler) agentUIRequest(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	var body struct {
		Request   string `json:"request"`
		RequestID string `json:"request_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	request := strings.TrimSpace(body.Request)
	switch {
	case request == "":
		errBadRequest(w, "request is required")
		return
	case len(request) > maxUIRequestBytes:
		errBadRequest(w, fmt.Sprintf("request must be at most %d bytes", maxUIRequestBytes))
		return
	case !utf8.ValidString(request) || strings.ContainsRune(request, 0):
		errBadRequest(w, "request must be valid text")
		return
	case !uiRequestID.MatchString(body.RequestID):
		errBadRequest(w, "request_id must be 8-64 characters of letters, digits, '.', '_', ':' or '-'")
		return
	case strings.Contains(request, "<<<END_USER_REQUEST"):
		errBadRequest(w, "request must not contain Coral request markers")
		return
	}
	ls, err := h.ss.GetLiveSession(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if ls == nil {
		errNotFound(w, "live session not found")
		return
	}
	if _, status, message := h.validateExactLiveTarget(r.Context(), ls.AgentName, ls.AgentType, sid, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}
	sender, canPrompt := h.terminal.(ptymanager.PromptSender)
	if h.terminal == nil || !canPrompt {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"delivered": false, "error": "the terminal backend cannot deliver multi-line prompts"})
		return
	}
	// Canonical session name, the same target board nudges use; it resolves in
	// both the tmux and the PTY backends.
	terminalName := naming.SessionName(ls.AgentType, ls.SessionID)

	panelID := uiPanelID(body.RequestID)
	notification := buildUIPanelRequestNotification(body.RequestID, panelID, request, terminalName, h.uiRequestServerURL())
	record, outcome, err := h.db.BeginUIRequest(r.Context(), sid, body.RequestID, request, notification)
	if err != nil {
		errInternalServer(w, "could not record request")
		return
	}
	ok := func(duplicate bool, text string) map[string]any {
		return map[string]any{"ok": true, "delivered": true, "recorded": true, "duplicate": duplicate, "request_id": body.RequestID, "panel_id": panelID, "session_id": sid, "notification": text}
	}
	switch outcome {
	case store.UIRequestDuplicate:
		writeJSON(w, http.StatusOK, ok(true, record.Notification))
		return
	case store.UIRequestInProgress:
		writeJSON(w, http.StatusConflict, map[string]any{"delivered": false, "in_progress": true, "retryable": false, "error": "this request is already being delivered (or Coral stopped before recording the result); it was not resent", "request_id": body.RequestID})
		return
	case store.UIRequestUnknown:
		writeJSON(w, http.StatusConflict, map[string]any{"delivered": false, "delivery_unknown": true, "retryable": false, "error": "an earlier attempt may have partly reached the agent; it was not resent. Check the agent's input before sending again", "request_id": body.RequestID})
		return
	case store.UIRequestConflict:
		writeJSON(w, http.StatusConflict, map[string]any{"delivered": false, "error": "request_id was already used for a different request", "request_id": body.RequestID})
		return
	}

	sendCtx := r.Context()
	if enabled, known := bracketedPasteFromLog(h.cfg, ls.AgentType, ls.SessionID); known {
		sendCtx = ptymanager.WithBracketedPasteHint(sendCtx, enabled)
	}
	sendErr := sender.SendPrompt(sendCtx, terminalName, notification, ls.AgentType, ls.SessionID)
	// Record the result even if the client went away mid-request.
	finish := h.db.FinishUIRequest
	if uiRequestFinishOverride != nil {
		finish = uiRequestFinishOverride
	}
	finishErr := finish(context.WithoutCancel(r.Context()), sid, body.RequestID, sendErr)
	if sendErr != nil {
		switch {
		case errors.Is(sendErr, ptymanager.ErrDeliveryUnknown):
			// Input may already be in the agent's input line: never offer a retry.
			writeJSON(w, http.StatusBadGateway, map[string]any{"delivered": false, "delivery_unknown": true, "retryable": false, "error": sendErr.Error(), "request_id": body.RequestID})
		case errors.Is(sendErr, ptymanager.ErrBracketedPasteUnavailable):
			writeJSON(w, http.StatusConflict, map[string]any{"delivered": false, "error": sendErr.Error(), "request_id": body.RequestID, "retryable": true})
		default:
			// Nothing was sent (checked before any input), so a retry is safe.
			writeJSON(w, http.StatusBadGateway, map[string]any{"delivered": false, "error": sendErr.Error(), "request_id": body.RequestID, "retryable": true})
		}
		return
	}
	resp := ok(false, notification)
	if finishErr != nil {
		// The text WAS typed, so never claim a failure. The row stays "pending"
		// in the store, so a retry answers 409 instead of resending.
		log.Printf("[ui-request] delivered but could not record result for %s/%s: %v", sid, body.RequestID, finishErr)
		resp["recorded"] = false
		resp["warning"] = "the request was delivered but Coral could not record it; do not resend it"
	}
	writeJSON(w, http.StatusOK, resp)
}
