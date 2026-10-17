package routes

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// Detail returns detailed info for a specific live session.
// GET /api/sessions/live/{name}
func (h *SessionsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}

	logPath := h.findLogPath(agentType, sessionID)
	logInfo := h.cachedLogStatus(logPath) // #3711: kept between reads like the list's

	paneText, _ := h.terminal.CaptureOutput(r.Context(), name, 200, agentType, sessionID)

	out := map[string]any{
		"name":              name,
		"session_id":        sessionID,
		"status":            logInfo["status"],
		"summary":           logInfo["summary"],
		"recent_lines":      logInfo["recent_lines"],
		"staleness_seconds": logInfo["staleness_seconds"],
		"pane_capture":      paneText,
	}
	if sessionID != "" {
		for k, v := range h.autoAcceptPayload(r.Context(), sessionID) {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Capture returns a tmux pane capture for a session.
// GET /api/sessions/live/{name}/capture
func (h *SessionsHandler) Capture(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}

	text, err := h.terminal.CaptureOutput(r.Context(), name, 200, agentType, sessionID)
	if err != nil || text == "" {
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": nil, "error": "Could not capture pane"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": text})
}

// Poll returns capture, tasks, and events in a single batch response.
// GET /api/sessions/live/{name}/poll
func (h *SessionsHandler) Poll(w http.ResponseWriter, r *http.Request) {
	// With CORAL_DEBUG_PHASES=1, each poll logs its phases like the live list and
	// chat (coral-hub's tunnel measurements).
	trace := newPhaseTrace("live-poll")
	defer trace.finish()
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	trace.begin("target_check")
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}
	trace.end("target_check")
	eventsLimit := clampLimit(queryInt(r, "events_limit", 50), 50, 200)

	ctx := r.Context()

	// Capture pane
	trace.begin("capture")
	captureResult := map[string]any{"name": name, "capture": nil}
	if text, err := h.captureForPoll(ctx, name, agentType, sessionID); err == nil && text != "" {
		captureResult["capture"] = text
	} else {
		captureResult["error"] = fmt.Sprintf("Could not capture pane for '%s'", name)
	}
	trace.end("capture")

	// Tasks
	trace.begin("tasks")
	var tasks any = []any{}
	if sessionID != "" {
		if t, err := h.ts.ListAgentTasks(ctx, name, &sessionID); err == nil {
			tasks = t
		}
	}
	trace.endPool("tasks", h.db)

	// Events
	trace.begin("events")
	var events any = []any{}
	if e, err := h.ts.ListAgentEvents(ctx, name, eventsLimit, ptr.NonEmpty(sessionID)); err == nil {
		events = e
	}
	trace.endPool("events", h.db)

	// Polled every 500 ms by an open terminal: an unchanged answer is a 304 with no body (#3439).
	httputil.WriteJSONValidated(w, r, http.StatusOK, map[string]any{
		"capture": captureResult,
		"tasks":   tasks,
		"events":  events,
	})
}

type cachedCapture struct {
	stamp string
	text  string
	at    time.Time
}

// captureReuseMax bounds how long a capture is reused on an unchanged output stamp: a few screen
// changes print nothing to the pane's output log (a resize reflowing the screen, tmux
// clear-history, copy-mode if anything ever uses it), and show at most this late.
const captureReuseMax = 5 * time.Second

// captureCacheMax caps the cache; it starts over when full (a burst of short-lived sessions).
const captureCacheMax = 256

// captureForPoll is CaptureOutput for the poll (sharedCapture's 200 lines).
func (h *SessionsHandler) captureForPoll(ctx context.Context, name, agentType, sessionID string) (string, error) {
	return h.sharedCapture(ctx, name, 200, agentType, sessionID)
}

// sharedCapture is CaptureOutput for read-only readers (the poll, the login-screen check, the
// team status's activity line; #4077): when the backend can stamp the session's output and
// nothing was written since the last capture of these lines (and that is recent), the last
// capture is reused, whoever asked for it. The stamp is taken before capturing, so output
// printed meanwhile is seen next time. Readers that act on the screen (prompt answers, pastes,
// auto-accept, the input gate) capture fresh and never come here.
func (h *SessionsHandler) sharedCapture(ctx context.Context, name string, lines int, agentType, sessionID string) (string, error) {
	fetch := func(c context.Context) (string, error) {
		return h.terminal.CaptureOutput(c, name, lines, agentType, sessionID)
	}
	// Tabs polling this agent share one capture for a moment (#3887), but only while the pane's
	// output is unchanged: the output stamp is part of the sharing key, so a poll that sees new
	// output never gets a capture started before it (#3954: typed output showed up to 2 s late).
	// Without a stamp nothing tells the captures apart, so none is shared. The key is the
	// session, not the name a caller used for it, so callers naming it differently share.
	stamper, _ := h.backend.(ptymanager.OutputStamper)
	if stamper == nil || agentType == "" || sessionID == "" {
		return fetch(ctx)
	}
	stampedAt := time.Now()
	stamp, ok := stamper.OutputStamp(agentType, sessionID)
	if !ok {
		return fetch(ctx)
	}
	key := agentType + "\x00" + sessionID + "\x00" + strconv.Itoa(lines)
	h.captureMu.Lock()
	c, hit := h.captureCache[key]
	h.captureMu.Unlock()
	if hit && c.stamp == stamp && time.Since(c.at) < captureReuseMax {
		return c.text, nil
	}
	text, capturedAt, err := h.screens.capture(ctx, key+"\x00"+stamp, lines, time.Now(), fetch)
	h.captureMu.Lock()
	defer h.captureMu.Unlock()
	// Kept against the stamp only when the capture started after the stamp was read: a shared
	// capture from before it may miss output the stamp already counts.
	if err == nil && text != "" && !capturedAt.Before(stampedAt) {
		if h.captureCache == nil || len(h.captureCache) >= captureCacheMax {
			h.captureCache = map[string]cachedCapture{}
		}
		h.captureCache[key] = cachedCapture{stamp: stamp, text: text, at: time.Now()}
	} else {
		delete(h.captureCache, key)
	}
	return text, err
}

// Chat returns the JSONL conversation transcript.
// GET /api/sessions/live/{name}/chat
func (h *SessionsHandler) Chat(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("live-chat")
	defer trace.finish()
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	workingDir := r.URL.Query().Get("working_directory")
	var resumeFromID, agentName string
	var resumeLookupDone bool
	metaCache := "not_needed" // hit: from memory; miss: read from the sessions DB (#3438)
	trace.begin("metadata_lookup")

	// If missing metadata, attempt lookup by session_id or name: from memory when it was
	// read recently (#3438: a chat through Coral Hub always lacks the working directory,
	// and the sessions DB has one connection shared with every background writer).
	if agentType == "" || workingDir == "" || sessionID == "" {
		targetID := sessionID
		if targetID == "" {
			targetID = name
		}
		metaCache = "miss"
		if m, ok := h.chatMeta.get(targetID); ok {
			metaCache = "hit"
			resumeLookupDone = true
			resumeFromID = m.resumeFromID
			agentName = m.agentName
			if agentType == "" {
				agentType = m.agentType
			}
			if workingDir == "" {
				workingDir = m.workingDir
			}
			if sessionID == "" {
				sessionID = m.sessionID
			}
		}
	}
	if (agentType == "" || workingDir == "" || sessionID == "") && h.ss != nil && !resumeLookupDone {
		targetID := sessionID
		if targetID == "" {
			targetID = name
		}
		if ls, err := h.ss.GetLiveSession(r.Context(), targetID); err == nil && ls != nil {
			h.chatMeta.put(chatMeta{agentType: ls.AgentType, workingDir: ls.WorkingDir, sessionID: ls.SessionID, resumeFromID: ptr.Value(ls.ResumeFromID), agentName: ls.AgentName}, targetID, ls.SessionID)
			agentName = ls.AgentName
			resumeLookupDone = true
			resumeFromID = ptr.Value(ls.ResumeFromID)
			h.rememberResumeFromID(ls.SessionID, resumeFromID)
			if agentType == "" && ls.AgentType != "" {
				agentType = ls.AgentType
			}
			if workingDir == "" && ls.WorkingDir != "" {
				workingDir = ls.WorkingDir
			}
			if sessionID == "" {
				sessionID = ls.SessionID
			}
		} else if all, err := h.ss.GetAllLiveSessions(r.Context()); err == nil {
			matches := 0
			for _, s := range all {
				if s.AgentName == name || s.SessionID == name {
					matches++
				}
			}
			for _, s := range all {
				if s.AgentName == name || s.SessionID == name {
					// Cached under the name only when it names one session: an agent name is a
					// folder name, which several agents can share (the first match is used, as
					// before, but not pinned).
					keys := []string{s.SessionID}
					if matches == 1 {
						keys = append(keys, name)
					}
					h.chatMeta.put(chatMeta{agentType: s.AgentType, workingDir: s.WorkingDir, sessionID: s.SessionID, resumeFromID: ptr.Value(s.ResumeFromID), agentName: s.AgentName}, keys...)
					agentName = s.AgentName
					resumeLookupDone = true
					resumeFromID = ptr.Value(s.ResumeFromID)
					h.rememberResumeFromID(s.SessionID, resumeFromID)
					if agentType == "" && s.AgentType != "" {
						agentType = s.AgentType
					}
					if workingDir == "" && s.WorkingDir != "" {
						workingDir = s.WorkingDir
					}
					if sessionID == "" {
						sessionID = s.SessionID
					}
					break
				}
			}
		}
	}
	// Live chat clients normally provide all session metadata explicitly. Even
	// in that case, load the persisted resume lineage so a replacement session
	// can read its trusted ancestor rollout until the new marker is emitted.
	if !resumeLookupDone && sessionID != "" {
		resumeFromID = h.cachedResumeFromID(r.Context(), sessionID)
	}
	// A request that names the session by its id (the dashboard starts a deep-linked chat's history before the live list has said the agent's name,
	// #4326) is answered as the request with the agent's own name: the prompt, login and screen reads below go by name.
	if sessionID != "" && name == sessionID && agentName != "" {
		name = agentName
	}
	trace.endPool("metadata_lookup", h.db, "meta_cache", metaCache)

	if agentType == "" {
		agentType = at.Claude
	}

	// Use session_id if provided, otherwise use name as session_id
	id := sessionID
	if id == "" {
		id = name
	}

	// When after=0, the client wants the full conversation history (e.g. after
	// switching to the Chat tab). Return all accumulated messages, not just new ones.
	// Supports pagination: ?after=0&limit=100&offset=0 returns the last 100 messages.
	// Otherwise return messages[after:]. The reader's cache is shared by every
	// client, so "new since the last read" would split new messages between
	// windows polling the same session; each client's own count is used instead.
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	// Live requests carry an exact session ID. Antigravity's native
	// conversation IDs can also be UUIDs, so strict marker validation belongs
	// only on this explicit live-session path, not generic history browsing.
	var messages []map[string]any
	var total int
	trace.begin("transcript_read")
	if sessionID != "" {
		if resumeFromID != "" {
			messages, total = h.jsonl.ReadAllMessagesForLiveWithLineageContext(r.Context(), id, workingDir, agentType, resumeFromID)
		} else {
			messages, total = h.jsonl.ReadAllMessagesForLiveContext(r.Context(), id, workingDir, agentType)
		}
	} else {
		messages, total = h.jsonl.ReadAllMessagesContext(r.Context(), id, workingDir, agentType)
	}
	trace.end("transcript_read", "messages", total)
	trace.begin("log_status")
	// Reuse the bounded log-status cache used by the live-session list. Chat
	// polls run every second; reparsing the same 256KB log tail on each poll
	// adds avoidable filesystem and parsing work when the log is unchanged.
	// A pending sign-in is said by its own card; the limit notice only when there is none (#3830).
	var needsLogin bool
	var loginReason string
	if agentType == at.Claude {
		needsLogin, loginReason = h.claudeLoginState(name, sessionID, workingDir, func() []store.AgentEvent {
			byID, _ := h.ts.GetSessionStateEvents(r.Context(), []string{sessionID})
			return byID[sessionID]
		})
	}
	limitNotice := ""
	if !needsLogin {
		limitNotice = h.usageLimitText(agentType, sessionID, workingDir, h.cachedLogStatus(h.findLogPath(agentType, sessionID)))
	}
	trace.end("log_status")
	messages = markCoralDeliveries(messages, agentType, sessionID, name) // the whole transcript: a delivery is used by one turn (#4306)
	if key := r.URL.Query().Get("find_key"); key != "" {
		writeKeyLookup(w, messageKeySecretFor(h.cfg), messages, key) // Save for Later: is that message still in the transcript, and where (#4510)
		return
	}
	if after > 0 {
		messages = messages[min(after, len(messages)):]
	}
	messages = emptyIfNil(messages)

	// Pagination for full history: return the `limit` messages before a
	// cursor. `before` is an absolute message index (exclusive), so an older
	// page stays put while new messages are appended; pass the previous page's
	// `start` to get the page above it. `offset` counts backwards from the
	// current end and is kept for older clients, but it shifts whenever a
	// message arrives between two pages. `start` is the absolute index of the
	// first message returned. An empty transcript is a page too (start 0), and
	// a bad before is refused whether or not there are messages.
	if after == 0 {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100 // default page size
		}
		end := len(messages)
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err := strconv.Atoi(raw)
			if err != nil || before < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "before must be a non-negative integer"})
				return
			}
			end = min(before, end)
		} else {
			// offset=0 means the most recent messages; offset=100 means skip the last 100
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			end = max(end-max(offset, 0), 0)
		}
		start := max(end-limit, 0)
		page := withKeys(messageKeySecretFor(h.cfg), messages[start:end]) // each message's stable key, for Save for Later (#4510)
		if trim, _ := strconv.Atoi(r.URL.Query().Get("trim")); trim > 0 {
			page = trimChatPage(page, trim) // long tool fields cut, marked (chat_trim.go, #3723)
		}
		reply := map[string]any{
			"messages":     page,
			"total":        total,
			"start":        start,
			"has_more":     start > 0,
			"limit_notice": limitNotice,
			"needs_login":  needsLogin,
			"login_reason": nilIf(!needsLogin || loginReason == "", loginReason),
		}
		h.addNeedsInput(r, reply, name, agentType, sessionID)
		httputil.WriteJSONCompressed(w, r, http.StatusOK, reply)
		return
	}

	reply := map[string]any{"messages": withKeys(messageKeySecretFor(h.cfg), messages), "total": total, "limit_notice": limitNotice,
		"needs_login": needsLogin, "login_reason": nilIf(!needsLogin || loginReason == "", loginReason)}
	h.addNeedsInput(r, reply, name, agentType, sessionID)
	writeJSON(w, http.StatusOK, reply)
}

// Info returns enriched metadata for the session info modal.
// GET /api/sessions/live/{name}/info
// addTranscriptModel sets "model" to the model the session's transcript last recorded, when
// one was read (#4065): the model the agent actually runs, after a /model switch too. Without
// one, "model" stays the model it was launched or started with, if any. model_source says which.
func (h *SessionsHandler) addTranscriptModel(entry map[string]any, sessionID string) {
	if h.jsonl == nil || sessionID == "" {
		return
	}
	if m := h.jsonl.TranscriptModel(sessionID); m != "" {
		entry["model"], entry["model_source"] = m, "transcript"
	} else if _, ok := entry["model"]; ok {
		entry["model_source"] = "launch"
	}
}

func (h *SessionsHandler) Info(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	ctx := r.Context()

	pane, _ := h.terminal.FindSession(ctx, name, agentType, sessionID)

	result := map[string]any{
		"name":       name,
		"session_id": sessionID,
		"agent_name": name,
		"agent_type": agentType,
	}

	if pane != nil {
		result["tmux_session_name"] = pane.SessionName
		result["pane_title"] = pane.PaneTitle
		result["working_directory"] = pane.CurrentPath
		result["tmux_command"] = h.terminal.AttachCommand(pane.SessionName)
	}

	// Include log path and other metadata from live session record
	if sessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
			if ls.DisplayName != nil && *ls.DisplayName != "" {
				result["agent_name"] = *ls.DisplayName
			}
			result["agent_type"] = ls.AgentType
			// Construct log path from agent type + session ID
			logPath := naming.LogFile(h.cfg.LogDir, ls.AgentType, sessionID)
			result["log_path"] = logPath
			if ls.Model != nil && *ls.Model != "" {
				result["model"] = *ls.Model
			}
		}
		h.addTranscriptModel(result, sessionID)
	}

	// Look up git state by session_id first, then by name
	var git *store.GitSnapshot
	if sessionID != "" {
		var gitErr error
		git, gitErr = h.gs.GetLatestGitStateBySession(ctx, sessionID)
		if gitErr != nil {
			slog.Warn("failed to get git state by session", "session_id", sessionID, "error", gitErr)
		}
	}
	if git == nil {
		var gitErr error
		git, gitErr = h.gs.GetLatestGitState(ctx, name)
		if gitErr != nil {
			slog.Warn("failed to get git state by name", "agent_name", name, "error", gitErr)
		}
	}
	if git != nil {
		result["git_branch"] = git.Branch
		result["git_commit_hash"] = git.CommitHash
		result["git_commit_subject"] = git.CommitSubject
		if git.RemoteURL != nil {
			if rn := gitutil.ParseRepoName(*git.RemoteURL); rn != "" {
				result["git_repo"] = rn
			}
		}
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

// SessionStatus returns lifecycle and agent-turn state for one stable session ID.
// GET /api/sessions/{sessionID}/status
func (h *SessionsHandler) SessionStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\\x00") {
		errBadRequest(w, "invalid session_id")
		return
	}

	var row struct {
		Status     string  `db:"status"`
		IsSleeping int     `db:"is_sleeping"`
		StoppedAt  *string `db:"stopped_at"`
	}
	err := h.db.Reader().GetContext(r.Context(), &row,
		"SELECT status, is_sleeping, stopped_at FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		var indexed bool
		if indexErr := h.db.Reader().GetContext(r.Context(), &indexed,
			"SELECT EXISTS(SELECT 1 FROM session_index WHERE session_id = ?)", sessionID); indexErr != nil || !indexed {
			errNotFound(w, "session not found")
			return
		}
		row.Status = "inactive"
	} else if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	finished := row.Status != "active"
	sleeping := !finished && row.IsSleeping == 1
	latestEvent, latestSummary := "", ""
	if events, eventErr := h.ts.GetLatestEventTypes(r.Context(), []string{sessionID}); eventErr == nil {
		latestEvent = events[sessionID][0]
		latestSummary = events[sessionID][1]
	}
	// Shared derivation: only a request made directly to the user is
	// "waiting_for_input". A finished turn, including the idle reminder that
	// follows it, is "done" here, which is this endpoint's name for the
	// list row's awaiting_user.
	var liveState SessionState
	if !finished && !sleeping {
		liveState = h.deriveSingleSessionState(r.Context(), sessionID, 0)
	}
	waiting := liveState.NeedsInput
	done := liveState.AwaitingUser

	state := "active"
	switch {
	case finished:
		state = "finished"
	case sleeping:
		state = "sleeping"
	case waiting:
		state = "waiting_for_input"
	case done:
		state = "done"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":        sessionID,
		"state":             state,
		"active":            !finished,
		"finished":          finished,
		"waiting_for_input": waiting,
		"awaiting_user":     done,
		"waiting_reason":    ptr.NullIfEmpty(liveState.WaitingReason),
		"waiting_summary":   ptr.NullIfEmpty(liveState.WaitingSummary),
		"waiting_kind":      ptr.NullIfEmpty(liveState.WaitingKind),
		"done":              done,
		"sleeping":          sleeping,
		"latest_event":      ptr.NullIfEmpty(latestEvent),
		"latest_summary":    ptr.NullIfEmpty(latestSummary),
		"stopped_at":        row.StoppedAt,
	})
}
