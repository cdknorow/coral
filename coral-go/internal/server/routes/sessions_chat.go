package routes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/clitext"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
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
	trace.begin("metadata_lookup")
	t := h.resolveChatTarget(r, chi.URLParam(r, "name"), r.URL.Query().Get("agent_type"), r.URL.Query().Get("session_id"), r.URL.Query().Get("working_directory"))
	trace.endPool("metadata_lookup", h.db, "meta_cache", t.metaCache)
	name, agentType, sessionID, workingDir := t.name, t.agentType, t.sessionID, t.workingDir

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
	trace.begin("transcript_read")
	tv := h.readChatTranscript(r.Context(), id, t)
	messages, total := tv.Window, tv.Total
	trace.end("transcript_read", "messages", total)
	trace.begin("log_status")
	needsLogin, loginReason, limitNotice := h.chatNotices(r, name, agentType, sessionID, workingDir)
	trace.end("log_status")
	messages = markCoralDeliveries(messages, agentType, sessionID, name) // the window: a delivery is used by one turn (#4306)
	if key := r.URL.Query().Get("find_key"); key != "" {
		writeKeyLookup(w, r.Context(), messageKeySecretFor(h.cfg), tv, key) // Save for Later: is that message still in the transcript, and where (#4510)
		return
	}
	if after > 0 {
		messages = chatSlice(r.Context(), tv, messages, after, total)
	}
	messages = emptyIfNil(messages)

	if after == 0 {
		page, start, err := h.chatHistoryPage(r, tv, messages, total)
		if err != nil {
			errBadRequest(w, err.Error())
			return
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

// chatTarget is the session a chat request reads: the name its prompt, login and screen reads go by, its agent
// type, id, working directory and resume lineage, and where its metadata came from (for the trace).
type chatTarget struct {
	name, agentType, sessionID, workingDir string
	resumeFromID                           string
	metaCache                              string // not_needed; hit: from memory; miss: read from the sessions DB (#3438)
}

// resolveChatTarget completes the request's metadata. Missing fields come from memory when they were read recently
// (#3438: a chat through Coral Hub always lacks the working directory, and the sessions DB has one connection shared
// with every background writer), else from the live session by id or name, else from the first live session with
// that name (cached under the name only when one session has it). A request with all of it still loads the resume
// lineage. A request naming the session by its id is answered as the one with the agent's own name.
func (h *SessionsHandler) resolveChatTarget(r *http.Request, name, agentType, sessionID, workingDir string) chatTarget {
	var resumeFromID, agentName string
	var resumeLookupDone bool
	metaCache := "not_needed"

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
		// fill takes what a live session row has for the fields still missing, and remembers it under keys.
		fill := func(s store.LiveSession, keys ...string) {
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
		}
		if ls, err := h.ss.GetLiveSession(r.Context(), targetID); err == nil && ls != nil {
			fill(*ls, targetID, ls.SessionID)
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
					fill(s, keys...)
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
	if agentType == "" {
		agentType = at.Claude
	}
	return chatTarget{name: name, agentType: agentType, sessionID: sessionID, workingDir: workingDir, resumeFromID: resumeFromID, metaCache: metaCache}
}

// readChatTranscript reads the transcript of the session id names. Live requests carry an exact session ID.
// Antigravity's native conversation IDs can also be UUIDs, so strict marker validation belongs only on this
// explicit live-session path, not generic history browsing. The reader keeps a session's newest messages (its
// window) and counts the rest: every index is the message's number in the whole transcript, and a page that reaches
// before the window is read from the file (#4620).
func (h *SessionsHandler) readChatTranscript(ctx context.Context, id string, t chatTarget) *jsonl.Transcript {
	if t.sessionID == "" {
		return h.jsonl.ReadTranscriptContext(ctx, id, t.workingDir, t.agentType)
	}
	if t.resumeFromID != "" {
		return h.jsonl.ReadTranscriptForLiveWithLineageContext(ctx, id, t.workingDir, t.agentType, t.resumeFromID)
	}
	return h.jsonl.ReadTranscriptForLiveContext(ctx, id, t.workingDir, t.agentType)
}

// chatNotices are the chat's sign-in state (Claude's) and its usage-limit notice: a pending sign-in is said by its
// own card, so the limit notice only when there is none (#3830). It reuses the bounded log-status cache used by the
// live-session list: chat polls run every second, and reparsing the same 256KB log tail on each poll adds avoidable
// filesystem and parsing work when the log is unchanged.
func (h *SessionsHandler) chatNotices(r *http.Request, name, agentType, sessionID, workingDir string) (needsLogin bool, loginReason, limitNotice string) {
	if agentType == at.Claude {
		needsLogin, loginReason = h.claudeLoginState(name, sessionID, workingDir, func() []store.AgentEvent {
			byID, _ := h.ts.GetSessionStateEvents(r.Context(), []string{sessionID})
			return byID[sessionID]
		})
	}
	if !needsLogin {
		limitNotice = h.usageLimitText(agentType, sessionID, workingDir, h.cachedLogStatus(h.findLogPath(agentType, sessionID)))
	}
	return needsLogin, loginReason, limitNotice
}

// errChatBefore is a history page asked for before a bad index.
var errChatBefore = errors.New("before must be a non-negative integer")

// chatHistoryPage is the page of full history a request asks for: the `limit` messages before a
// cursor. `before` is an absolute message index (exclusive), so an older
// page stays put while new messages are appended; pass the previous page's
// `start` to get the page above it. `offset` counts backwards from the
// current end and is kept for older clients, but it shifts whenever a
// message arrives between two pages. `start` is the absolute index of the
// first message returned. An empty transcript is a page too (start 0), and
// a bad before is refused whether or not there are messages.
func (h *SessionsHandler) chatHistoryPage(r *http.Request, tv *jsonl.Transcript, messages []map[string]any, total int) (page []map[string]any, start int, err error) {
	// 100 by default, never more than 500 a page (#4602: a huge limit was the whole transcript, and a copy of it).
	limit := clampLimit(queryInt(r, "limit", 100), 100, 500)
	end := total
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err := strconv.Atoi(raw)
		if err != nil || before < 0 {
			return nil, 0, errChatBefore
		}
		end = min(before, end)
	} else {
		// offset=0 means the most recent messages; offset=100 means skip the last 100
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end = max(end-max(offset, 0), 0)
	}
	start = max(end-limit, 0)
	page = withKeys(messageKeySecretFor(h.cfg), chatSlice(r.Context(), tv, messages, start, end)) // each message's stable key, for Save for Later (#4510)
	if trim, _ := strconv.Atoi(r.URL.Query().Get("trim")); trim > 0 {
		page = trimChatPage(page, trim, requestRedactor(r)) // long tool fields cut, marked (chat_trim.go, #3723)
	}
	return page, start, nil
}

// Info returns enriched metadata for the session info modal.
// GET /api/sessions/live/{name}/info
// addTranscriptModel sets "model" to the model the session's transcript last recorded, when
// one was read (#4065): the model the agent actually runs, after a /model switch too. Without
// one, "model" is the model a hook last reported (agy, which has no transcript model), then
// the model it was launched or started with, if any. model_source says which.
func (h *SessionsHandler) addTranscriptModel(entry map[string]any, sessionID string, observed *string) {
	if sessionID == "" {
		return
	}
	m, t := h.transcriptModel(sessionID), h.transcriptThinking(sessionID)
	// What the CLI was last given or confirmed (a /model or /effort Claude took, a wake that started it with the stored ones) is the agent's own until the transcript records a turn written after it (#6225):
	// the chip no longer says "not confirmed yet" after a confirmed switch, nor after a restart or wake.
	if stamp, ok := h.ss.SessionSettingsStamp(context.Background(), sessionID); ok && (h.jsonl == nil || h.jsonl.TranscriptTurnAt(sessionID).Before(stamp)) {
		if stored, _ := entry["thinking"].(string); stored != "" {
			t = stored
		}
		if stored, _ := entry["model"].(string); stored != "" {
			m = stored
		}
	}
	if t != "" {
		entry["thinking_actual"] = t // the level the newest turn ran at, as the CLI's own transcript records it (#4969)
	}
	h.addModelSwitch(entry, sessionID)
	if m != "" {
		entry["model"], entry["model_source"] = m, "transcript"
	} else if o := ptr.Value(observed); o != "" {
		entry["model"], entry["model_source"] = o, "hook"
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
		var observed *string
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
			if ls.Thinking != nil && *ls.Thinking != "" {
				result["thinking"] = *ls.Thinking
			}
			observed = ls.ObservedModel
		}
		h.addTranscriptModel(result, sessionID, observed)
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
		result["git_branch"] = clitext.CleanName(git.Branch)
		result["git_commit_hash"] = git.CommitHash
		result["git_commit_subject"] = clitext.Clean(git.CommitSubject)
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
		"waiting_summary":   waitingLine(waitingSource(liveState.WaitingWants, liveState.WaitingSummary)),
		"waiting_kind":      ptr.NullIfEmpty(liveState.WaitingKind),
		"done":              done,
		"sleeping":          sleeping,
		"latest_event":      ptr.NullIfEmpty(latestEvent),
		"latest_summary":    ptr.NullIfEmpty(latestSummary),
		"stopped_at":        row.StoppedAt,
	})
}
