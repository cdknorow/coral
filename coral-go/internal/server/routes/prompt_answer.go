package routes

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Answering an agent's prompt from the chat.
//
// Claude Code's permission prompts, AskUserQuestion dialogs and plan
// approvals draw a numbered option list at the bottom of the terminal, with
// "❯" marking the selected option; pressing an option's digit answers it
// (a multi-question dialog advances to the next question, then to a
// "Submit answers" review). The option list varies (permission choices
// depend on the tool and mode), so it is read from the screen rather than
// assumed, and an answer is only sent after re-reading the screen and
// confirming the digit still maps to the option the user clicked.

type promptOption struct {
	N        int    `json:"n"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	FreeText bool   `json:"free_text"` // needs typed input; answer in the terminal
}

type promptScreen struct {
	Question string         `json:"question"`
	Options  []promptOption `json:"options"`
}

var (
	promptOptionRe = regexp.MustCompile(`^\s*(❯\s*)?(\d{1,2})\.\s+(.+?)\s*$`)
	separatorRe    = regexp.MustCompile(`^\s*[─━╌┄═]+\s*$`)
)

// Options that open a text field instead of answering
var freeTextPrefixes = []string{"type something", "chat about this", "tell claude what to change"}

func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// parsePromptScreen reads the option block of an open prompt from the bottom
// of a terminal capture. It walks up from the last option line through the
// options, their wrapped/description lines (indented five or more) and
// separators, and stops at the prompt's own question line. A numbered list
// elsewhere on screen (a plan's steps, a reply) is never reached, and a block
// without the "❯" selection marker is not a prompt.
func parsePromptScreen(capture string) (promptScreen, bool) {
	lines := strings.Split(strings.ReplaceAll(capture, "\r", ""), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	// The option list ends within a few lines of the bottom (a footer such
	// as "Enter to select · Esc to cancel" may follow it).
	end := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-6; i-- {
		if promptOptionRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return promptScreen{}, false
	}
	var opts []promptOption
	question := ""
	for i := end; i >= 0; i-- {
		line := lines[i]
		if m := promptOptionRe.FindStringSubmatch(line); m != nil && indentOf(line) < 5 {
			n, _ := strconv.Atoi(m[2])
			label := m[3]
			lower := strings.ToLower(label)
			free := false
			for _, p := range freeTextPrefixes {
				if strings.HasPrefix(lower, p) {
					free = true
				}
			}
			opts = append([]promptOption{{N: n, Label: label, Selected: m[1] != "", FreeText: free}}, opts...)
			continue
		}
		if strings.TrimSpace(line) == "" || separatorRe.MatchString(line) || indentOf(line) >= 5 {
			continue
		}
		question = strings.TrimSpace(line)
		break
	}
	if len(opts) < 2 {
		return promptScreen{}, false
	}
	selected := false
	for i, o := range opts {
		if o.N != i+1 {
			return promptScreen{}, false // not a clean 1..k list
		}
		selected = selected || o.Selected
	}
	if !selected {
		return promptScreen{}, false
	}
	return promptScreen{Question: question, Options: opts}, true
}

func (h *SessionsHandler) capturePromptScreen(r *http.Request, name, agentType, sessionID string) (promptScreen, bool) {
	text, err := h.terminal.CaptureOutput(r.Context(), name, 80, agentType, sessionID)
	if err != nil || text == "" {
		return promptScreen{}, false
	}
	return parsePromptScreen(text)
}

// PromptOptions returns the options of the prompt open in the agent's
// terminal, or {"options": []} when none is open.
// GET /api/sessions/live/{name}/prompt-options?session_id=...&agent_type=...
func (h *SessionsHandler) PromptOptions(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}
	screen, ok := h.capturePromptScreen(r, name, agentType, sessionID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"question": "", "options": []promptOption{}})
		return
	}
	writeJSON(w, http.StatusOK, screen)
}

func normalizeLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// AnswerPrompt selects an option of the prompt open in the agent's terminal.
// The screen is re-read first: the digit is sent only if that option still
// has the label the user clicked, so a prompt that changed or closed is
// never answered by mistake.
// POST /api/sessions/live/{name}/answer-prompt {"session_id","agent_type","n","label"}
func (h *SessionsHandler) AnswerPrompt(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID string `json:"session_id"`
		AgentType string `json:"agent_type"`
		N         int    `json:"n"`
		Label     string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.N < 1 || body.N > 9 || strings.TrimSpace(body.Label) == "" {
		errBadRequest(w, "n (1-9) and label are required")
		return
	}
	if shouldValidateExactTarget(body.AgentType, body.SessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}
	screen, ok := h.capturePromptScreen(r, name, body.AgentType, body.SessionID)
	if !ok || body.N > len(screen.Options) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "The prompt is no longer open. Check the terminal."})
		return
	}
	opt := screen.Options[body.N-1]
	if opt.FreeText || normalizeLabel(opt.Label) != normalizeLabel(body.Label) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "The prompt changed. Answer it in the terminal."})
		return
	}
	if err := h.terminal.SendRawInput(r.Context(), name, []string{strconv.Itoa(body.N)}, body.AgentType, body.SessionID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answered": opt.Label})
}
