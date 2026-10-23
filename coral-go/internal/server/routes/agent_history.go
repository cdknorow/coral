package routes

import (
	"context"
	"fmt"
	"github.com/subgentic/coral/pkg/redact"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/store"
)

// AgentHistoryMatch represents a single matching turn found in session history.
type AgentHistoryMatch struct {
	SessionID    string   `json:"session_id"`
	IsCurrent    bool     `json:"is_current"`
	MessageIndex int      `json:"message_index"`
	Role         string   `json:"role"`
	Timestamp    string   `json:"timestamp"`
	Excerpt      string   `json:"excerpt"`
	MatchOffsets [][]int  `json:"match_offsets,omitempty"`
	MatchTerms   []string `json:"match_terms,omitempty"`
}

// maxHistoryMatches bounds the matches one search collects across a lineage before it sorts and pages them: each excerpt is bounded,
// their number was not, so a common term over a long lineage held every match in memory for the request. The response says
// truncated when the search stopped there (a var for the tests).
var maxHistoryMatches = 5000

// SessionSearchStatus describes the search readiness for each session in a lineage.
type SessionSearchStatus struct {
	SessionID        string `json:"session_id"`
	Role             string `json:"role"`   // "current" or "ancestor"
	Status           string `json:"status"` // "ready", "transcript_unavailable", "empty"
	MessagesSearched int    `json:"messages_searched"`
	Error            string `json:"error,omitempty"`
}

// AgentHistorySearchResponse is the API contract for history search.
type AgentHistorySearchResponse struct {
	Query            string                `json:"query"`
	Status           string                `json:"status"` // "complete", "partial", "unavailable"
	TotalMatches     int                   `json:"total_matches"`
	Truncated        bool                  `json:"truncated,omitempty"` // the search stopped at maxHistoryMatches: total_matches is the number kept, there are more
	HasMore          bool                  `json:"has_more"`
	Limit            int                   `json:"limit"`
	Offset           int                   `json:"offset"`
	Results          []AgentHistoryMatch   `json:"results"`
	SessionsSearched []SessionSearchStatus `json:"sessions_searched"`
}

// AgentHistoryContextMessage is a single message returned in surrounding context.
type AgentHistoryContextMessage struct {
	MessageIndex int    `json:"message_index"`
	Role         string `json:"role"`
	Timestamp    string `json:"timestamp"`
	Content      string `json:"content"`
	IsTarget     bool   `json:"is_target"`
}

// AgentHistoryContextResponse is the API contract for surrounding context retrieval.
type AgentHistoryContextResponse struct {
	SessionID     string                       `json:"session_id"`
	TargetIndex   int                          `json:"target_index"`
	TotalMessages int                          `json:"total_messages"`
	Window        int                          `json:"window"`
	Messages      []AgentHistoryContextMessage `json:"messages"`
}

// SearchAgentHistory searches the authenticated agent's own session history
// (including proven resume_from_id ancestors).
// GET /api/agent/history/search
func (h *SessionsHandler) SearchAgentHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sessionID, ok := historyCaller(w, r, q)
	if !ok {
		return
	}
	p, refused := parseHistorySearch(r, q)
	if refused != nil {
		refused(w)
		return
	}
	sessions, refused := h.historySearchSessions(r.Context(), sessionID, p)
	if refused != nil {
		refused(w)
		return
	}
	phrases, terms := parseSearchQuery(p.query)
	var allMatches []AgentHistoryMatch
	var sessionsSearched []SessionSearchStatus
	truncated := false
	for _, item := range sessions {
		room := maxHistoryMatches - len(allMatches)
		if room <= 0 { // the cap is reached: the sessions left are not read
			truncated = true
			break
		}
		status, matches, more := h.searchHistorySession(r.Context(), item, p, phrases, terms, room)
		sessionsSearched = append(sessionsSearched, status)
		allMatches = append(allMatches, matches...)
		if more {
			truncated = true
			break
		}
	}
	sortHistoryMatches(allMatches)
	results, hasMore := pageHistoryMatches(allMatches, p.offset, p.limit)
	writeJSON(w, http.StatusOK, AgentHistorySearchResponse{
		Query:            p.query,
		Status:           historySearchStatus(sessionsSearched),
		TotalMatches:     len(allMatches),
		Truncated:        truncated,
		HasMore:          hasMore,
		Limit:            p.limit,
		Offset:           p.offset,
		Results:          results,
		SessionsSearched: sessionsSearched,
	})
}

// historySearch is a history search's request: the query, the roles searched, which sessions of the lineage
// (all, the current one only, or one named), the page and the excerpt length.
type historySearch struct {
	query            string
	roles            map[string]bool
	includeAncestors bool
	targetSessionID  string
	limit, offset    int
	maxExcerptChars  int
}

// parseHistorySearch reads a history search's parameters: the query (query or q, required, at most 1000
// characters), the roles (assistant and user by default), include_ancestors (on unless false, 0 or no), a target
// session, the page (limit 20, at most 100) and the excerpt length (50 to 1000, 300 by default).
func parseHistorySearch(r *http.Request, q url.Values) (historySearch, answer) {
	p := historySearch{query: strings.TrimSpace(q.Get("query")), includeAncestors: true}
	if p.query == "" {
		p.query = strings.TrimSpace(q.Get("q"))
	}
	if p.query == "" {
		return p, func(w http.ResponseWriter) { errBadRequest(w, "query parameter 'query' or 'q' is required") }
	}
	if len(p.query) > 1000 {
		return p, func(w http.ResponseWriter) { errBadRequest(w, "query exceeds maximum length of 1000 characters") }
	}
	rolesParam := q.Get("roles")
	if rolesParam == "" {
		rolesParam = "assistant,user"
	}
	p.roles = parseRoles(rolesParam)
	if inc := q.Get("include_ancestors"); inc == "false" || inc == "0" || inc == "no" {
		p.includeAncestors = false
	}
	p.targetSessionID = strings.TrimSpace(q.Get("target_session_id"))
	p.limit = clampLimit(queryInt(r, "limit", 20), 20, 100)
	p.offset = clampOffset(queryInt(r, "offset", 0))
	p.maxExcerptChars = min(max(queryInt(r, "max_excerpt_chars", 300), 50), 1000)
	return p, nil
}

// historySearchSessions are the sessions a search reads: the caller's proven lineage (its own session and its
// resume_from_id ancestors), or only its current session, or one session of the lineage it names (refused when the
// name is outside it).
func (h *SessionsHandler) historySearchSessions(ctx context.Context, sessionID string, p historySearch) ([]store.SessionLineageItem, answer) {
	ancestry, err := h.ss.GetSessionAncestry(ctx, sessionID)
	if err != nil {
		return nil, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	}
	if len(ancestry) == 0 {
		return nil, func(w http.ResponseWriter) { errNotFound(w, "Unknown session; run this from inside a Coral agent") }
	}
	if p.targetSessionID != "" {
		for _, item := range ancestry {
			if item.SessionID == p.targetSessionID {
				return []store.SessionLineageItem{item}, nil
			}
		}
		return nil, func(w http.ResponseWriter) {
			errForbidden(w, "Unauthorized: session is not within the authenticated session lineage")
		}
	}
	if !p.includeAncestors {
		return ancestry[:1], nil
	}
	return ancestry, nil
}

// searchHistorySession searches one session's transcript: its status (ready with the messages searched, empty, or
// its transcript unavailable with why) and each message of an allowed role whose text matches, with an excerpt
// around the first match.
func (h *SessionsHandler) searchHistorySession(ctx context.Context, item store.SessionLineageItem, p historySearch, phrases, terms []string, room int) (SessionSearchStatus, []AgentHistoryMatch, bool) {
	status := SessionSearchStatus{SessionID: item.SessionID, Role: "ancestor"}
	if item.IsCurrent {
		status.Role = "current"
	}
	messages, errMsg, err := h.loadSessionMessages(ctx, item)
	switch {
	case err != nil:
		status.Status, status.Error = "transcript_unavailable", err.Error()
		return status, nil, false
	case errMsg != "":
		status.Status, status.Error = "transcript_unavailable", errMsg
		return status, nil, false
	case len(messages) == 0:
		status.Status = "empty"
		return status, nil, false
	}
	status.Status, status.MessagesSearched = "ready", len(messages)
	var matches []AgentHistoryMatch
	for idx, msg := range messages {
		msgRole, _ := msg["type"].(string)
		if !p.roles[msgRole] {
			continue
		}
		text := extractSearchableMessageText(msg)
		if text == "" {
			continue
		}
		matched, offsets, matchedTerms := matchMessage(text, phrases, terms)
		if !matched {
			continue
		}
		if len(matches) >= room { // one match more than the room left: the search is cut here
			return status, matches, true
		}
		firstOffset := 0
		if len(offsets) > 0 {
			firstOffset = offsets[0][0]
		}
		timestamp, _ := msg["timestamp"].(string)
		matches = append(matches, AgentHistoryMatch{
			SessionID:    item.SessionID,
			IsCurrent:    item.IsCurrent,
			MessageIndex: idx,
			Role:         msgRole,
			Timestamp:    timestamp,
			Excerpt:      makeBoundedExcerpt(text, firstOffset, p.maxExcerptChars),
			MatchOffsets: offsets,
			MatchTerms:   matchedTerms,
		})
	}
	return status, matches, false
}

// sortHistoryMatches orders matches newest first; at the same time (or one unparsable), the current session's
// before an ancestor's, then the later message first.
func sortHistoryMatches(matches []AgentHistoryMatch) {
	sort.SliceStable(matches, func(i, j int) bool {
		ti, errI := parseFlexibleTimestamp(matches[i].Timestamp)
		tj, errJ := parseFlexibleTimestamp(matches[j].Timestamp)
		if errI == nil && errJ == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		if matches[i].IsCurrent != matches[j].IsCurrent {
			return matches[i].IsCurrent
		}
		return matches[i].MessageIndex > matches[j].MessageIndex
	})
}

// historySearchStatus is complete when every session searched was ready, partial when some were, and unavailable
// when none was.
func historySearchStatus(sessions []SessionSearchStatus) string {
	allReady, anyReady := true, false
	for _, s := range sessions {
		if s.Status == "ready" {
			anyReady = true
		} else {
			allReady = false
		}
	}
	switch {
	case !anyReady:
		return "unavailable"
	case !allReady:
		return "partial"
	}
	return "complete"
}

// pageHistoryMatches is the requested page of the matches (never nil), and whether more follow it.
func pageHistoryMatches(matches []AgentHistoryMatch, offset, limit int) ([]AgentHistoryMatch, bool) {
	start := min(offset, len(matches))
	end := min(start+limit, len(matches))
	results := matches[start:end]
	if results == nil {
		results = []AgentHistoryMatch{}
	}
	return results, end < len(matches)
}

// GetAgentHistoryContext retrieves a window of conversation turns surrounding a target turn.
// GET /api/agent/history/context
func (h *SessionsHandler) GetAgentHistoryContext(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	sessionID, ok := historyCaller(w, r, q)
	if !ok {
		return
	}

	msgIndexStr := q.Get("message_index")
	if msgIndexStr == "" {
		errBadRequest(w, "message_index parameter is required")
		return
	}
	targetIndex, err := strconv.Atoi(msgIndexStr)
	if err != nil || targetIndex < 0 {
		errBadRequest(w, "message_index must be a non-negative integer")
		return
	}

	targetSessionID := strings.TrimSpace(q.Get("target_session_id"))
	if targetSessionID == "" {
		targetSessionID = sessionID
	}

	window := queryInt(r, "window", 2)
	if window < 1 {
		window = 1
	} else if window > 10 {
		window = 10
	}

	// Authenticate caller session and verify target session is within proven lineage
	ancestry, err := h.ss.GetSessionAncestry(r.Context(), sessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if len(ancestry) == 0 {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
		return
	}

	var targetItem *store.SessionLineageItem
	for i := range ancestry {
		if ancestry[i].SessionID == targetSessionID {
			targetItem = &ancestry[i]
			break
		}
	}
	if targetItem == nil {
		errForbidden(w, "Unauthorized: session is not within the authenticated session lineage")
		return
	}

	messages, errMsg, err := h.loadSessionMessages(r.Context(), *targetItem)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if errMsg != "" {
		errNotFound(w, errMsg)
		return
	}
	if len(messages) == 0 {
		errNotFound(w, "transcript for session is empty")
		return
	}

	if targetIndex >= len(messages) {
		errNotFound(w, fmt.Sprintf("message_index %d out of bounds (session has %d messages)", targetIndex, len(messages)))
		return
	}

	start := max(0, targetIndex-window)
	end := min(len(messages), targetIndex+window+1)

	var contextMessages []AgentHistoryContextMessage
	for idx := start; idx < end; idx++ {
		msg := messages[idx]
		role, _ := msg["type"].(string)
		ts, _ := msg["timestamp"].(string)
		content := extractFullMessageContent(msg)

		contextMessages = append(contextMessages, AgentHistoryContextMessage{
			MessageIndex: idx,
			Role:         role,
			Timestamp:    ts,
			Content:      content,
			IsTarget:     idx == targetIndex,
		})
	}

	resp := AgentHistoryContextResponse{
		SessionID:     targetSessionID,
		TargetIndex:   targetIndex,
		TotalMessages: len(messages),
		Window:        window,
		Messages:      contextMessages,
	}

	writeJSON(w, http.StatusOK, resp)
}

// SearchAgentHistoryForLive provides the live session endpoint for history search.
// GET /api/sessions/live/{name}/history/search
func (h *SessionsHandler) SearchAgentHistoryForLive(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		if ls, err := h.ss.GetLiveSession(r.Context(), name); err == nil && ls != nil {
			sessionID = ls.SessionID
		} else {
			sessionID = name
		}
	}

	q := r.URL.Query()
	q.Set("session_id", sessionID)
	r.URL.RawQuery = q.Encode()

	h.SearchAgentHistory(w, r)
}

// GetAgentHistoryContextForLive provides the live session endpoint for context retrieval.
// GET /api/sessions/live/{name}/history/context
func (h *SessionsHandler) GetAgentHistoryContextForLive(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		if ls, err := h.ss.GetLiveSession(r.Context(), name); err == nil && ls != nil {
			sessionID = ls.SessionID
		} else {
			sessionID = name
		}
	}

	q := r.URL.Query()
	q.Set("session_id", sessionID)
	r.URL.RawQuery = q.Encode()

	h.GetAgentHistoryContext(w, r)
}

// loadSessionMessages reads all parsed messages for a session lineage item.
func (h *SessionsHandler) loadSessionMessages(ctx context.Context, item store.SessionLineageItem) ([]map[string]any, string, error) {
	agentType := item.AgentType
	if agentType == "" {
		agentType = "claude"
	}

	if item.IsCurrent {
		var messages []map[string]any
		if item.SessionID != "" {
			messages, _ = h.jsonl.ReadAllMessagesForLiveContext(context.Background(), item.SessionID, item.WorkingDir, agentType)
		} else {
			messages, _ = h.jsonl.ReadAllMessagesContext(context.Background(), item.SessionID, item.WorkingDir, agentType)
		}
		if len(messages) > 0 {
			return messages, "", nil
		}

		path := jsonl.TranscriptPath(item.SessionID, item.WorkingDir, agentType)
		if path == "" || !fileExists(path) {
			return nil, fmt.Sprintf("live transcript not found for session %s", item.SessionID), nil
		}
		return messages, "", nil
	}

	// Historical ancestor session: prefer direct SourceFile if recorded
	if item.SourceFile != "" {
		if !fileExists(item.SourceFile) {
			return nil, fmt.Sprintf("historical transcript file %s does not exist", item.SourceFile), nil
		}
		messages, _, err := jsonl.ReadFromContext(context.Background(), item.SourceFile, agentType, 0)
		if err != nil {
			return nil, fmt.Sprintf("failed to read transcript file %s: %v", item.SourceFile, err), nil
		}
		return messages, "", nil
	}

	// Fallback to jsonl reader by sessionID
	messages, _ := h.jsonl.ReadAllMessagesContext(context.Background(), item.SessionID, item.WorkingDir, agentType)
	if len(messages) == 0 {
		return nil, fmt.Sprintf("no transcript discovered for ancestor session %s", item.SessionID), nil
	}
	return messages, "", nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func parseRoles(raw string) map[string]bool {
	m := make(map[string]bool)
	for _, r := range strings.Split(raw, ",") {
		r = strings.TrimSpace(strings.ToLower(r))
		if r != "" {
			m[r] = true
		}
	}
	if len(m) == 0 {
		m["user"] = true
		m["assistant"] = true
	}
	return m
}

func parseSearchQuery(raw string) (phrases []string, terms []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	inQuote := false
	var current strings.Builder
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			if inQuote {
				str := strings.TrimSpace(current.String())
				if str != "" {
					phrases = append(phrases, strings.ToLower(str))
				}
				current.Reset()
				inQuote = false
			} else {
				str := strings.TrimSpace(current.String())
				if str != "" {
					for _, t := range strings.Fields(str) {
						terms = append(terms, strings.ToLower(t))
					}
				}
				current.Reset()
				inQuote = true
			}
		} else {
			current.WriteByte(ch)
		}
	}
	remaining := strings.TrimSpace(current.String())
	if remaining != "" {
		if inQuote {
			phrases = append(phrases, strings.ToLower(remaining))
		} else {
			for _, t := range strings.Fields(remaining) {
				terms = append(terms, strings.ToLower(t))
			}
		}
	}
	return phrases, terms
}

func extractSearchableMessageText(msg map[string]any) string {
	var parts []string

	if text, ok := msg["text"].(string); ok && strings.TrimSpace(text) != "" {
		parts = append(parts, strings.TrimSpace(text))
	}
	if content, ok := msg["content"].(string); ok && strings.TrimSpace(content) != "" {
		c := strings.TrimSpace(content)
		if len(parts) == 0 || parts[0] != c {
			parts = append(parts, c)
		}
	}

	// Tool uses
	if rawToolUses, ok := msg["tool_uses"]; ok {
		switch tuList := rawToolUses.(type) {
		case []map[string]any:
			for _, tu := range tuList {
				appendToolUseText(&parts, tu)
			}
		case []any:
			for _, item := range tuList {
				if tu, ok := item.(map[string]any); ok {
					appendToolUseText(&parts, tu)
				}
			}
		}
	}

	return strings.Join(parts, " ")
}

func appendToolUseText(parts *[]string, tu map[string]any) {
	if desc, ok := tu["description"].(string); ok && strings.TrimSpace(desc) != "" {
		*parts = append(*parts, strings.TrimSpace(desc))
	}
	if cmd, ok := tu["command"].(string); ok && strings.TrimSpace(cmd) != "" {
		*parts = append(*parts, strings.TrimSpace(cmd))
	}
	if is, ok := tu["input_summary"].(string); ok && strings.TrimSpace(is) != "" {
		*parts = append(*parts, strings.TrimSpace(is))
	}
	if name, ok := tu["name"].(string); ok && strings.TrimSpace(name) != "" {
		*parts = append(*parts, strings.TrimSpace(name))
	}
}

func extractFullMessageContent(msg map[string]any) string {
	var parts []string
	if text, ok := msg["text"].(string); ok && strings.TrimSpace(text) != "" {
		parts = append(parts, strings.TrimSpace(text))
	}
	if content, ok := msg["content"].(string); ok && strings.TrimSpace(content) != "" {
		c := strings.TrimSpace(content)
		if len(parts) == 0 || parts[0] != c {
			parts = append(parts, c)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return extractSearchableMessageText(msg)
}

func matchMessage(text string, phrases, terms []string) (bool, [][]int, []string) {
	textLower := strings.ToLower(text)
	var matchedOffsets [][]int
	var matchedTerms []string

	for _, p := range phrases {
		idx := strings.Index(textLower, p)
		if idx == -1 {
			return false, nil, nil
		}
		matchedOffsets = append(matchedOffsets, []int{idx, idx + len(p)})
		matchedTerms = append(matchedTerms, p)
	}

	for _, t := range terms {
		idx := strings.Index(textLower, t)
		if idx == -1 {
			return false, nil, nil
		}
		matchedOffsets = append(matchedOffsets, []int{idx, idx + len(t)})
		matchedTerms = append(matchedTerms, t)
	}

	sort.Slice(matchedOffsets, func(i, j int) bool {
		return matchedOffsets[i][0] < matchedOffsets[j][0]
	})

	return true, matchedOffsets, matchedTerms
}

func makeBoundedExcerpt(text string, matchOffset int, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 300
	}
	clean := cleanWhitespace(text)
	runes := []rune(clean)
	if len(runes) <= maxChars {
		return clean
	}
	// Redact the whole text BEFORE the window is cut out of it: a window whose ends fall inside a secret leaves part of a token that the redactor no longer recognises (a GitHub token short of its last
	// character passed unchanged, #3733's class; Security's finding on #6207). Text that cannot be checked is not shown.
	red, err := redact.Rules.Text([]byte(clean))
	if err != nil {
		return redact.Marker
	}
	clean = string(red)
	runes = []rune(clean)
	if len(runes) <= maxChars {
		return clean
	}

	cleanLower := strings.ToLower(clean)
	subLen := 10
	if matchOffset+subLen > len(text) {
		subLen = len(text) - matchOffset
	}
	rMatchOffset := -1
	if subLen > 0 {
		rMatchOffset = strings.Index(cleanLower, strings.ToLower(text[matchOffset:matchOffset+subLen]))
	}
	if rMatchOffset < 0 {
		rMatchOffset = len(runes) / 2
	} else {
		rMatchOffset = len([]rune(clean[:rMatchOffset]))
	}

	halfWindow := maxChars / 2
	start := rMatchOffset - halfWindow
	if start < 0 {
		start = 0
	}
	end := start + maxChars
	if end > len(runes) {
		end = len(runes)
		start = max(0, end-maxChars)
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return snippet
}

func cleanWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func parseFlexibleTimestamp(ts string) (time.Time, error) {
	if ts == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown timestamp format: %s", ts)
}

// historyCaller is the session whose history a request searches or reads (#4512, Security's #4508 H1): the
// caller's own. With an agent's session token the token decides (a session_id naming another session is 403
// identity_mismatch, and none means the token's); without one only the Operator (the dashboard, the verified
// command line, an authentic Coral Hub request) names a session, which the live routes do from the agent's name.
func historyCaller(w http.ResponseWriter, r *http.Request, q url.Values) (string, bool) {
	claimed := strings.TrimSpace(q.Get("session_id"))
	if claimed == "" {
		claimed = strings.TrimSpace(r.Header.Get("X-Coral-Session-ID"))
	}
	sessionID, ok := callerSession(w, r, claimed, true, nil)
	if !ok {
		return "", false
	}
	if sessionID == "" {
		errBadRequest(w, "session_id parameter is required")
		return "", false
	}
	return sessionID, true
}
