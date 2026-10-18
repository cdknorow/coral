package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/store"
)

// Chat, split into resolving the session, reading its transcript, its notices and the history page (#4713), answers
// as chatBefore4713 (the handler at v1.4.92, below) does. Two handlers share one store, board and terminal, each with
// its own caches (the metadata cache a request warms decides how the next one resolves), and take the same requests
// in step. The sessions have Claude transcripts of 0 to 260 messages; some share an agent name, some resume an
// earlier one. Requests name a session by id, by name or not at all, with or without its type, id and directory,
// and ask for history pages (limit, before, offset, trim), new messages after an index, or a message by key. The
// status and body are compared. Each kind of answer has a floor.

func chatOracleSetup(t *testing.T) (old, cur *httptest.Server, sids []string, names []string) {
	_, h, _, ss := setupSessionsTestServer(t)
	h2 := NewSessionsHandler(h.db, h.cfg, nil, h.terminal, h.bs)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	ctx := context.Background()
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	for i, n := range []int{0, 3, 40, 130, 260, 7, 560, 12} {
		sid := fmt.Sprintf("%08x-4713-4000-8000-0000000000%02d", i, i)
		var b strings.Builder
		for k := 0; k < n; k++ {
			if k%2 == 0 && k > 0 { // the previous call's output, long enough for a trim to cut
				b.WriteString(line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("t%d", k-1), "content": strings.Repeat("output line ", 60)},
				}}}))
				continue
			}
			if k%2 == 0 {
				b.WriteString(line(map[string]any{"type": "user", "message": map[string]any{"content": fmt.Sprintf("question %d", k)}}))
			} else {
				b.WriteString(line(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": fmt.Sprintf("answer %d", k)},
					map[string]any{"type": "tool_use", "id": fmt.Sprintf("t%d", k), "name": "Bash", "input": map[string]any{"command": strings.Repeat("echo long ", 40+k%7)}},
				}}}))
			}
		}
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(b.String()), 0o644))
		name := []string{"alpha", "beta", "shared", "shared", "gamma", "delta", "epsilon", "orphan"}[i]
		if i == 7 { // a transcript Coral has no live session for: read by its id, as a Claude one by default
			sids, names = append(sids, sid), append(names, name)
			continue
		}
		agentType := "claude"
		if i == 3 {
			agentType = "codex" // two sessions share a name: which one a lookup finds shows in the reply
		}
		ls := &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: name, WorkingDir: projectDir}
		if i == 5 {
			prev := sids[3]
			ls.ResumeFromID = &prev // a replacement reading its ancestor's transcript too
		}
		require.NoError(t, ss.RegisterLiveSession(ctx, ls))
		sids, names = append(sids, sid), append(names, name)
	}
	mk := func(handler http.HandlerFunc) *httptest.Server {
		r := chi.NewRouter()
		r.Get("/api/sessions/live/{name}/chat", handler)
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)
		return srv
	}
	return mk(h.chatBefore4713), mk(h2.Chat), sids, names
}

func chatOracleRequest(rng *rand.Rand, sids, names []string) (string, url.Values) {
	i := rng.Intn(len(sids))
	q := url.Values{}
	var path string
	switch rng.Intn(4) {
	case 0, 1:
		path = sids[i]
	case 2:
		path = names[i]
	default:
		path = "nobody-here"
	}
	if rng.Intn(2) == 0 {
		q.Set("session_id", sids[i])
	}
	if rng.Intn(2) == 0 {
		q.Set("agent_type", "claude")
	}
	if rng.Intn(3) == 0 {
		q.Set("working_directory", "/nowhere") // a directory that isn't the transcript's: the reader finds it anyway, or not
	}
	set := func(k string, vals ...string) {
		if rng.Intn(3) == 0 {
			q.Set(k, vals[rng.Intn(len(vals))])
		}
	}
	set("after", "0", "0", "1", "5", "39", "200", "999", "x")
	set("limit", "1", "10", "100", "600", "0", "x")
	set("before", "0", "3", "50", "120", "1000", "-1", "x")
	set("offset", "0", "5", "100", "-3")
	set("trim", "0", "50", "301")
	if rng.Intn(15) == 0 {
		q.Set("find_key", "not-a-real-key")
	}
	return path, q
}

func chatGet(t *testing.T, srv *httptest.Server, path string, q url.Values) (int, string) {
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions/live/"+url.PathEscape(path)+"/chat?"+q.Encode(), nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func TestChatAgreesWithBefore4713(t *testing.T) {
	old, cur, sids, names := chatOracleSetup(t)
	kinds := map[string]int{}
	for seed := int64(1); seed <= 150; seed++ {
		rng := rand.New(rand.NewSource(seed))
		for k := 0; k < 6; k++ {
			path, q := chatOracleRequest(rng, sids, names)
			oc, ob := chatGet(t, old, path, q)
			nc, nb := chatGet(t, cur, path, q)
			require.Equal(t, oc, nc, "seed %d request %d: %s %s", seed, k, path, q.Encode())
			require.Equal(t, ob, nb, "seed %d request %d: %s %s", seed, k, path, q.Encode())
			var out map[string]any
			json.Unmarshal([]byte(nb), &out)
			after := q.Get("after")
			msgs, _ := out["messages"].([]any)
			switch {
			case nc == http.StatusBadRequest:
				kinds["bad before refused"]++
			case q.Get("find_key") != "":
				kinds["a message by key"]++
			case after == "" || after == "0" || after == "x":
				if out["has_more"] == true {
					kinds["history page with more"]++
				} else {
					kinds["history page, all of it"]++
				}
				if len(msgs) == 0 {
					kinds["empty page"]++
				}
				if strings.Contains(nb, `"trimmed":true`) {
					kinds["trimmed"]++
				}
			default:
				kinds["new messages after an index"]++
			}
			if len(msgs) > 0 && q.Get("session_id") == "" && path != sids[0] && !strings.HasPrefix(path, "0") {
				kinds["resolved by name"]++
			}
			if len(msgs) > 100 {
				kinds["a page of more than 100"]++
			}
			if len(msgs) > 0 && strings.HasSuffix(path, "07") {
				kinds["a transcript with no live session"]++
			}
			if len(msgs) > 0 && q.Get("session_id") == "" && strings.HasSuffix(path, "05") {
				kinds["a resumed session by id alone"]++
			}
		}
	}
	t.Logf("compared, per kind: %v", kinds)
	for _, k := range []string{"bad before refused", "a message by key", "history page with more", "history page, all of it", "empty page",
		"trimmed", "new messages after an index", "resolved by name", "a resumed session by id alone", "a page of more than 100",
		"a transcript with no live session"} {
		assert.GreaterOrEqual(t, kinds[k], 10, "%q went (almost) unexercised", k)
	}
}

// chatBefore4713 is Chat before #4713 split it (sessions_chat.go at v1.4.92), kept to compare with.
func (h *SessionsHandler) chatBefore4713(w http.ResponseWriter, r *http.Request) {
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
	// The reader keeps a session's newest messages (its window) and counts the rest: every index
	// here is the message's number in the whole transcript, and a page that reaches before the
	// window is read from the file (#4620).
	var tv *jsonl.Transcript
	trace.begin("transcript_read")
	if sessionID != "" {
		if resumeFromID != "" {
			tv = h.jsonl.ReadTranscriptForLiveWithLineageContext(r.Context(), id, workingDir, agentType, resumeFromID)
		} else {
			tv = h.jsonl.ReadTranscriptForLiveContext(r.Context(), id, workingDir, agentType)
		}
	} else {
		tv = h.jsonl.ReadTranscriptContext(r.Context(), id, workingDir, agentType)
	}
	messages, total := tv.Window, tv.Total
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
	messages = markCoralDeliveries(messages, agentType, sessionID, name) // the window: a delivery is used by one turn (#4306)
	if key := r.URL.Query().Get("find_key"); key != "" {
		writeKeyLookup(w, r.Context(), messageKeySecretFor(h.cfg), tv, key) // Save for Later: is that message still in the transcript, and where (#4510)
		return
	}
	if after > 0 {
		messages = chatSlice(r.Context(), tv, messages, after, total)
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
		// 100 by default, never more than 500 a page (#4602: a huge limit was the whole transcript, and a copy of it).
		limit := clampLimit(queryInt(r, "limit", 100), 100, 500)
		end := total
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
		page := withKeys(messageKeySecretFor(h.cfg), chatSlice(r.Context(), tv, messages, start, end)) // each message's stable key, for Save for Later (#4510)
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
