// Package routes implements HTTP handlers for the Coral API.
package routes

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/pulse"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// SessionsHandler handles all live session API endpoints.
type SessionsHandler struct {
	db    *store.DB
	ss    *store.SessionStore
	ts    *store.TaskStore
	cfg   *config.Config
	tmux  *tmux.Client

	// Deduplication state for status/summary events (mirrors Python _last_known)
	lastKnownMu sync.RWMutex
	lastKnown   map[string]lastKnownState
}

type lastKnownState struct {
	Status  string
	Summary string
}

// NewSessionsHandler creates a SessionsHandler with the given dependencies.
func NewSessionsHandler(db *store.DB, cfg *config.Config) *SessionsHandler {
	return &SessionsHandler{
		db:        db,
		ss:        store.NewSessionStore(db),
		ts:        store.NewTaskStore(db),
		cfg:       cfg,
		tmux:      tmux.NewClient(),
		lastKnown: make(map[string]lastKnownState),
	}
}

// ── Agent Discovery ─────────────────────────────────────────────────────

// AgentInfo represents a discovered live agent.
type AgentInfo struct {
	AgentType    string `json:"agent_type"`
	AgentName    string `json:"agent_name"`
	SessionID    string `json:"session_id"`
	TmuxSession  string `json:"tmux_session"`
	LogPath      string `json:"log_path"`
	WorkingDir   string `json:"working_directory"`
}

func (h *SessionsHandler) discoverAgents(ctx *http.Request) ([]AgentInfo, error) {
	panes, err := h.tmux.ListPanes(ctx.Context())
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var agents []AgentInfo

	for _, pane := range panes {
		agentType, sessionID := pulse.ParseSessionName(pane.SessionName)
		if agentType == "" || sessionID == "" {
			continue
		}
		if seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		agentName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
		if agentName == "" {
			agentName = sessionID[:8]
		}

		logPath := filepath.Join(h.cfg.LogDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))

		agents = append(agents, AgentInfo{
			AgentType:   agentType,
			AgentName:   agentName,
			SessionID:   sessionID,
			TmuxSession: pane.SessionName,
			LogPath:     logPath,
			WorkingDir:  pane.CurrentPath,
		})
	}

	return agents, nil
}

// getLogStatus reads a log file and extracts PULSE status/summary.
func getLogStatus(logPath string) map[string]any {
	result := map[string]any{
		"status":            nil,
		"summary":           nil,
		"staleness_seconds": nil,
		"recent_lines":      []string{},
	}

	info, err := os.Stat(logPath)
	if err != nil {
		return result
	}

	staleness := time.Since(info.ModTime()).Seconds()
	result["staleness_seconds"] = staleness

	// Read tail of the file (last ~256KB)
	const tailBytes = 256_000
	f, err := os.Open(logPath)
	if err != nil {
		return result
	}
	defer f.Close()

	fileSize := info.Size()
	start := int64(0)
	if fileSize > tailBytes {
		start = fileSize - tailBytes
	}
	f.Seek(start, 0)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return result
	}
	if start > 0 {
		raw = raw[start:]
	}

	// Split into lines, decode, strip ANSI
	rawLines := strings.Split(string(raw), "\n")
	if start > 0 && len(rawLines) > 0 {
		rawLines = rawLines[1:] // drop partial first line
	}

	cleanLines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		cleanLines = append(cleanLines, pulse.StripANSI(line))
	}

	parsed := pulse.ParseLogLines(cleanLines)
	if parsed.Status != "" {
		result["status"] = parsed.Status
	}
	if parsed.Summary != "" {
		result["summary"] = parsed.Summary
	}
	result["recent_lines"] = parsed.RecentLines

	return result
}

// ── List / Detail ───────────────────────────────────────────────────────

// List returns all live agent sessions with enriched metadata.
// GET /api/sessions/live
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.discoverAgents(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	ctx := r.Context()

	// Batch fetch enrichment data
	sessionIDs := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.SessionID != "" {
			sessionIDs = append(sessionIDs, a.SessionID)
		}
	}
	displayNames, _ := h.ss.GetDisplayNames(ctx, sessionIDs)

	var sessions []map[string]any
	for _, agent := range agents {
		logInfo := getLogStatus(agent.LogPath)

		status, _ := logInfo["status"].(string)
		summary, _ := logInfo["summary"].(string)
		staleness := logInfo["staleness_seconds"]

		entry := map[string]any{
			"name":               agent.AgentName,
			"agent_type":         agent.AgentType,
			"session_id":         agent.SessionID,
			"tmux_session":       agent.TmuxSession,
			"status":             nilIfEmpty(status),
			"summary":            nilIfEmpty(summary),
			"staleness_seconds":  staleness,
			"working_directory":  agent.WorkingDir,
			"display_name":       displayNames[agent.SessionID],
			"branch":             nil,
			"waiting_for_input":  false,
			"waiting_reason":     nil,
			"waiting_summary":    nil,
			"working":            false,
			"changed_file_count": 0,
			"board_project":      nil,
			"board_job_title":    nil,
			"board_unread":       0,
			"log_path":           agent.LogPath,
		}

		// Track status/summary for event deduplication
		h.trackStatusSummary(ctx, agent.AgentName, status, summary, agent.SessionID)

		sessions = append(sessions, entry)
	}

	if sessions == nil {
		sessions = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, sessions)
}

func (h *SessionsHandler) trackStatusSummary(ctx interface{}, agentName, status, summary, sessionID string) {
	h.lastKnownMu.Lock()
	defer h.lastKnownMu.Unlock()

	key := sessionID
	if key == "" {
		key = agentName
	}

	prev := h.lastKnown[key]
	if status != "" && status != prev.Status {
		// TODO: store.InsertAgentEvent(agentName, "status", status, sessionID)
	}
	if summary != "" && summary != prev.Summary {
		// TODO: store.InsertAgentEvent(agentName, "goal", summary, sessionID)
	}
	h.lastKnown[key] = lastKnownState{Status: status, Summary: summary}
}

// Detail returns detailed info for a specific live session.
// GET /api/sessions/live/{name}
func (h *SessionsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")

	logPath := h.findLogPath(agentType, sessionID)
	logInfo := getLogStatus(logPath)

	paneText, _ := h.tmux.CapturePane(r.Context(), name, 200, agentType, sessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"name":              name,
		"session_id":        sessionID,
		"status":            logInfo["status"],
		"summary":           logInfo["summary"],
		"recent_lines":      logInfo["recent_lines"],
		"staleness_seconds": logInfo["staleness_seconds"],
		"pane_capture":      paneText,
	})
}

// Capture returns a tmux pane capture for a session.
// GET /api/sessions/live/{name}/capture
func (h *SessionsHandler) Capture(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")

	text, err := h.tmux.CapturePane(r.Context(), name, 200, agentType, sessionID)
	if err != nil || text == "" {
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": nil, "error": "Could not capture pane"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": text})
}

// Poll returns capture, tasks, and events in a single batch response.
// GET /api/sessions/live/{name}/poll
func (h *SessionsHandler) Poll(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	eventsLimit := queryInt(r, "events_limit", 50)
	if eventsLimit > 200 {
		eventsLimit = 200
	}

	ctx := r.Context()

	// Capture pane
	captureResult := map[string]any{"name": name, "capture": nil}
	if text, err := h.tmux.CapturePane(ctx, name, 200, agentType, sessionID); err == nil && text != "" {
		captureResult["capture"] = text
	} else {
		captureResult["error"] = fmt.Sprintf("Could not capture pane for '%s'", name)
	}

	// Tasks
	var tasks any = []any{}
	if sessionID != "" {
		if t, err := h.ts.ListAgentTasks(ctx, name, &sessionID); err == nil {
			tasks = t
		}
	}

	// Events
	var events any = []any{}
	if e, err := h.ts.ListAgentEvents(ctx, name, eventsLimit, strPtr(sessionID)); err == nil {
		events = e
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"capture": captureResult,
		"tasks":   tasks,
		"events":  events,
	})
}

// Chat returns the JSONL conversation transcript.
// GET /api/sessions/live/{name}/chat
func (h *SessionsHandler) Chat(w http.ResponseWriter, r *http.Request) {
	// TODO: port JsonlSessionReader
	writeJSON(w, http.StatusOK, map[string]any{"messages": []any{}, "total": 0})
}

// Info returns enriched metadata for the session info modal.
// GET /api/sessions/live/{name}/info
func (h *SessionsHandler) Info(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	ctx := r.Context()

	pane, _ := h.tmux.FindPane(ctx, name, agentType, sessionID)

	result := map[string]any{
		"name":       name,
		"session_id": sessionID,
	}

	if pane != nil {
		result["tmux_session"] = pane.SessionName
		result["pane_title"] = pane.PaneTitle
		result["current_path"] = pane.CurrentPath
		result["attach_command"] = fmt.Sprintf("tmux attach -t %s", pane.SessionName)
	}

	// Include prompt and board info from live session record
	if sessionID != "" {
		if info, err := h.ss.GetLiveSessionPromptInfo(ctx, sessionID); err == nil && info != nil {
			if info.Prompt != nil {
				result["prompt"] = *info.Prompt
			}
			if info.BoardName != nil {
				result["board_name"] = *info.BoardName
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// ── Files / Git ─────────────────────────────────────────────────────────

func (h *SessionsHandler) Files(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": []any{}})
}

func (h *SessionsHandler) RefreshFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": []any{}})
}

func (h *SessionsHandler) Diff(w http.ResponseWriter, r *http.Request) {
	fp := r.URL.Query().Get("filepath")
	writeJSON(w, http.StatusOK, map[string]any{"filepath": fp, "diff": ""})
}

func (h *SessionsHandler) SearchFiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"files": []any{}})
}

func (h *SessionsHandler) Git(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "snapshots": []any{}})
}

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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No command provided"})
		return
	}

	if err := h.tmux.SendKeys(r.Context(), name, body.Command, body.AgentType, body.SessionID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keys must be a non-empty list"})
		return
	}

	if err := h.tmux.SendRawKeys(r.Context(), name, body.Keys, body.AgentType, body.SessionID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Columns < 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "columns must be >= 10"})
		return
	}

	if err := h.tmux.ResizePane(r.Context(), name, body.Columns, body.AgentType, body.SessionID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "columns": body.Columns})
}

// ── Lifecycle ───────────────────────────────────────────────────────────

// Kill terminates a tmux session.
// POST /api/sessions/live/{name}/kill
func (h *SessionsHandler) Kill(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	if err := h.tmux.KillSession(r.Context(), name, body.AgentType, body.SessionID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Unregister from live sessions DB
	if body.SessionID != "" {
		h.ss.UnregisterLiveSession(r.Context(), body.SessionID)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Restart restarts the agent session.
// POST /api/sessions/live/{name}/restart
func (h *SessionsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType  string `json:"agent_type"`
		ExtraFlags string `json:"extra_flags"`
		SessionID  string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	ctx := r.Context()
	agentType := body.AgentType
	if agentType == "" {
		agentType = "claude"
	}

	pane, err := h.tmux.FindPane(ctx, name, agentType, body.SessionID)
	if err != nil || pane == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Pane not found"})
		return
	}

	newSessionID := generateUUID()
	newSessionName := fmt.Sprintf("%s-%s", agentType, newSessionID)
	newLogPath := filepath.Join(h.cfg.LogDir, fmt.Sprintf("%s_coral_%s.log", agentType, newSessionID))

	// Close old pipe-pane, respawn, rename
	h.tmux.ClosePipePane(ctx, pane.Target)
	if err := h.tmux.RespawnPane(ctx, pane.Target, pane.CurrentPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := h.tmux.RenameSession(ctx, pane.SessionName, newSessionName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	target := fmt.Sprintf("%s:0.0", newSessionName)
	time.Sleep(500 * time.Millisecond)

	// Clear scrollback, create log, setup pipe-pane
	h.tmux.ClearHistory(ctx, target)
	os.WriteFile(newLogPath, []byte{}, 0644)
	h.tmux.PipePane(ctx, target, newLogPath)

	// Set pane title
	folderName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
	titleCmd := fmt.Sprintf(`printf '\033]2;%s — %s\033\\'`, folderName, agentType)
	h.tmux.SendKeysToTarget(ctx, target, titleCmd)
	time.Sleep(300 * time.Millisecond)

	// Build and send launch command
	agentImpl := agent.GetAgent(agentType)
	protocolPath := h.protocolPath()
	var flags []string
	if body.ExtraFlags != "" {
		flags = strings.Fields(body.ExtraFlags)
	}
	cmd := agentImpl.BuildLaunchCommand(newSessionID, protocolPath, "", flags, pane.CurrentPath)
	h.tmux.SendKeysToTarget(ctx, target, cmd)

	// Replace live session in DB
	h.ss.ReplaceLiveSession(ctx, body.SessionID, &store.LiveSession{
		SessionID:    newSessionID,
		AgentType:    agentType,
		AgentName:    folderName,
		WorkingDir:   pane.CurrentPath,
		ResumeFromID: strPtr(body.SessionID),
	})
	h.ss.MigrateDisplayName(ctx, body.SessionID, newSessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": newSessionID, "session_name": newSessionName,
	})
}

// Resume restarts with --resume to continue a historical session.
// POST /api/sessions/live/{name}/resume
func (h *SessionsHandler) Resume(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID        string `json:"session_id"`
		AgentType        string `json:"agent_type"`
		CurrentSessionID string `json:"current_session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id is required"})
		return
	}

	agentType := body.AgentType
	if agentType == "" {
		agentType = "claude"
	}
	agentImpl := agent.GetAgent(agentType)
	if !agentImpl.SupportsResume() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Resume not supported for %s", agentType)})
		return
	}

	ctx := r.Context()
	pane, _ := h.tmux.FindPane(ctx, name, agentType, body.CurrentSessionID)
	if pane == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Pane not found"})
		return
	}

	// Prepare resume files
	agentImpl.PrepareResume(body.SessionID, pane.CurrentPath)

	newSessionID := generateUUID()
	newSessionName := fmt.Sprintf("%s-%s", agentType, newSessionID)
	newLogPath := filepath.Join(h.cfg.LogDir, fmt.Sprintf("%s_coral_%s.log", agentType, newSessionID))

	h.tmux.ClosePipePane(ctx, pane.Target)
	h.tmux.RespawnPane(ctx, pane.Target, pane.CurrentPath)
	h.tmux.RenameSession(ctx, pane.SessionName, newSessionName)

	target := fmt.Sprintf("%s:0.0", newSessionName)
	time.Sleep(500 * time.Millisecond)
	h.tmux.ClearHistory(ctx, target)
	os.WriteFile(newLogPath, []byte{}, 0644)
	h.tmux.PipePane(ctx, target, newLogPath)

	cmd := agentImpl.BuildLaunchCommand(newSessionID, h.protocolPath(), body.SessionID, nil, pane.CurrentPath)
	h.tmux.SendKeysToTarget(ctx, target, cmd)

	h.ss.ReplaceLiveSession(ctx, body.CurrentSessionID, &store.LiveSession{
		SessionID:    newSessionID,
		AgentType:    agentType,
		AgentName:    filepath.Base(strings.TrimRight(pane.CurrentPath, "/")),
		WorkingDir:   pane.CurrentPath,
		ResumeFromID: strPtr(body.SessionID),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": newSessionID, "session_name": newSessionName,
	})
}

// Attach opens a native terminal attached to the tmux session.
// POST /api/sessions/live/{name}/attach
func (h *SessionsHandler) Attach(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	pane, _ := h.tmux.FindPane(r.Context(), name, body.AgentType, body.SessionID)
	if pane == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Pane not found"})
		return
	}

	// Open Terminal.app attached to the tmux session (macOS)
	go func() {
		cmd := fmt.Sprintf(`tell application "Terminal" to do script "tmux attach -t %s"`, pane.SessionName)
		exec.Command("osascript", "-e", cmd).Run()
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SetDisplayName sets the display name for a live session.
// PUT /api/sessions/live/{name}/display-name
func (h *SessionsHandler) SetDisplayName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
		SessionID   string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.SessionID == "" || body.DisplayName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id and display_name required"})
		return
	}

	if err := h.ss.SetDisplayName(r.Context(), body.SessionID, body.DisplayName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "display_name": body.DisplayName})
}

// Launch creates a new agent session.
// POST /api/sessions/launch
func (h *SessionsHandler) Launch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkingDir  string   `json:"working_dir"`
		AgentType   string   `json:"agent_type"`
		DisplayName string   `json:"display_name"`
		Flags       []string `json:"flags"`
		Prompt      string   `json:"prompt"`
		BoardName   string   `json:"board_name"`
		BoardServer string   `json:"board_server"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.WorkingDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "working_dir is required"})
		return
	}

	result, err := h.launchSession(r.Context(), body.WorkingDir, body.AgentType, body.DisplayName,
		"", body.Flags, body.Prompt, body.BoardName, body.BoardServer)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Setup board and prompt in background
	if body.BoardName != "" || body.Prompt != "" {
		go h.setupBoardAndPrompt(result["session_id"].(string), result["session_name"].(string),
			body.AgentType, body.Prompt, body.BoardName, body.DisplayName)
	}

	writeJSON(w, http.StatusOK, result)
}

// LaunchTeam launches multiple agents on a shared message board.
// POST /api/sessions/launch-team
func (h *SessionsHandler) LaunchTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BoardName   string   `json:"board_name"`
		WorkingDir  string   `json:"working_dir"`
		AgentType   string   `json:"agent_type"`
		Flags       []string `json:"flags"`
		BoardServer string   `json:"board_server"`
		Agents      []struct {
			Name   string `json:"name"`
			Prompt string `json:"prompt"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.BoardName == "" || body.WorkingDir == "" || len(body.Agents) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "board_name, working_dir, and agents required"})
		return
	}

	ctx := r.Context()
	var launched []map[string]any

	for _, agentDef := range body.Agents {
		if agentDef.Name == "" {
			continue
		}
		result, err := h.launchSession(ctx, body.WorkingDir, body.AgentType, agentDef.Name,
			"", body.Flags, agentDef.Prompt, body.BoardName, body.BoardServer)
		if err != nil {
			launched = append(launched, map[string]any{"name": agentDef.Name, "error": err.Error()})
			continue
		}

		go h.setupBoardAndPrompt(result["session_id"].(string), result["session_name"].(string),
			body.AgentType, agentDef.Prompt, body.BoardName, agentDef.Name)

		launched = append(launched, map[string]any{
			"name": agentDef.Name, "session_id": result["session_id"], "session_name": result["session_name"],
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "board": body.BoardName, "agents": launched})
}

// ── Tasks ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *SessionsHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string `json:"title"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	name := chi.URLParam(r, "name")
	now := time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": 0, "agent_name": name, "title": body.Title,
		"completed": false, "created_at": now,
	})
}

func (h *SessionsHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) ReorderTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Notes ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListNotes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *SessionsHandler) CreateNote(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Events ──────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *SessionsHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventType  string `json:"event_type"`
		Summary    string `json:"summary"`
		ToolName   string `json:"tool_name"`
		SessionID  string `json:"session_id"`
		DetailJSON any    `json:"detail_json"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.EventType == "" || body.Summary == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_type and summary required"})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": 0, "event_type": body.EventType, "summary": body.Summary, "created_at": now,
	})
}

func (h *SessionsHandler) EventCounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *SessionsHandler) ClearEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// WebSocket handlers are in websocket.go

// ── Helpers ─────────────────────────────────────────────────────────────

func (h *SessionsHandler) findLogPath(agentType, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if agentType == "" {
		agentType = "claude"
	}
	return filepath.Join(h.cfg.LogDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("JSON encode error: %v", err)
	}
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func generateUUID() string {
	b := make([]byte, 16)
	crand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// launchSession creates a new tmux session with pipe-pane logging and launches an agent.
func (h *SessionsHandler) launchSession(ctx context.Context, workDir, agentType, displayName, resumeSessionID string,
	flags []string, prompt, boardName, boardServer string) (map[string]any, error) {

	absDir, err := filepath.Abs(workDir)
	if err != nil || !isDir(absDir) {
		return nil, fmt.Errorf("directory not found: %s", workDir)
	}

	if agentType == "" {
		agentType = "claude"
	}
	folderName := filepath.Base(absDir)

	sessionID := generateUUID()
	sessionName := fmt.Sprintf("%s-%s", agentType, sessionID)
	logFile := filepath.Join(h.cfg.LogDir, fmt.Sprintf("%s_coral_%s.log", agentType, sessionID))

	isTerminal := agentType == "terminal"
	agentImpl := agent.GetAgent(agentType)
	if resumeSessionID != "" && !isTerminal {
		agentImpl.PrepareResume(resumeSessionID, absDir)
	}

	// Create empty log file
	os.WriteFile(logFile, []byte{}, 0644)

	// Create tmux session
	if err := h.tmux.NewSession(ctx, sessionName, absDir); err != nil {
		return nil, fmt.Errorf("tmux new-session failed: %w", err)
	}

	// Setup pipe-pane logging
	h.tmux.PipePane(ctx, sessionName, logFile)

	// Set pane title
	titleCmd := fmt.Sprintf(`printf '\033]2;%s — %s\033\\'`, folderName, agentType)
	h.tmux.SendKeysToTarget(ctx, sessionName+".0", titleCmd)
	time.Sleep(300 * time.Millisecond)

	// Launch the agent (unless terminal)
	if !isTerminal {
		cmd := agentImpl.BuildLaunchCommand(sessionID, h.protocolPath(), resumeSessionID, flags, absDir)
		h.tmux.SendKeysToTarget(ctx, sessionName+".0", cmd)
	}

	// Register in DB
	h.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:    sessionID,
		AgentType:    agentType,
		AgentName:    folderName,
		WorkingDir:   absDir,
		DisplayName:  strPtr(displayName),
		ResumeFromID: strPtr(resumeSessionID),
		Flags:        store.MarshalFlags(flags),
		Prompt:       strPtr(prompt),
		BoardName:    strPtr(boardName),
		BoardServer:  strPtr(boardServer),
	})

	if displayName != "" {
		h.ss.SetDisplayName(ctx, sessionID, displayName)
	}

	return map[string]any{
		"ok": true, "session_id": sessionID, "session_name": sessionName, "log_file": logFile,
	}, nil
}

// setupBoardAndPrompt subscribes to a board and sends the initial prompt.
func (h *SessionsHandler) setupBoardAndPrompt(sessionID, sessionName, agentType, prompt, boardName, displayName string) {
	time.Sleep(3 * time.Second) // Wait for agent to initialize

	role := displayName
	if role == "" {
		role = agentType
	}

	// Build the full prompt with board instructions
	fullPrompt := prompt
	if boardName != "" {
		boardInstructions := fmt.Sprintf(`
You are subscribed to message board "%s". Your role is: %s. Use the coral-board CLI to communicate with your teammates:
  coral-board read          — read new messages from teammates
  coral-board post "msg"    — post a message to the board
  coral-board read --last 5 — see the 5 most recent messages
  coral-board subscribers   — see who is on the board
Check the board periodically for updates from your teammates.`, boardName, role)

		if fullPrompt != "" {
			fullPrompt += "\n\n" + boardInstructions
		} else {
			fullPrompt = boardInstructions
		}
	}

	if fullPrompt != "" {
		ctx := context.Background()
		err := h.tmux.SendKeys(ctx, agentType, fullPrompt, agentType, sessionID)
		if err != nil {
			log.Printf("Failed to send prompt to %s: %v", sessionID[:8], err)
		}
	}
}

func (h *SessionsHandler) protocolPath() string {
	// Look for PROTOCOL.md relative to the binary or in known locations
	candidates := []string{
		filepath.Join(h.cfg.CoralRoot, "PROTOCOL.md"),
		filepath.Join(h.cfg.CoralRoot, "src", "coral", "PROTOCOL.md"),
	}
	// Also check near the executable
	if ex, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(ex), "PROTOCOL.md"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
