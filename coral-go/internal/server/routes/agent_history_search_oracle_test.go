package routes

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/store"
)

// The outcomes the history-search oracle must reach, each counted from the condition happening, with a floor.
var historySearchOutcomes = map[string]int{
	"no query":                                 10,
	"query too long":                           5,
	"no session named":                         5,
	"unknown session":                          10,
	"target outside the lineage":               10,
	"complete: every session ready":            40,
	"partial: some transcript unavailable":     20,
	"unavailable: nothing readable":            5,
	"an empty transcript":                      10,
	"an ancestor's match":                      40,
	"a quoted phrase matched":                  20,
	"several terms matched":                    20,
	"a page past the end":                      10,
	"more after this page":                     20,
	"a role filter left one role":              20,
	"current before ancestor at the same time": 10,
	"an unparsable timestamp listed":           10,
	"a target session searched alone":          20,
	"the current session only":                 20,
	"the excerpt clamped short":                10,
}

// #4834: SearchAgentHistory split into parseHistorySearch, historySearchSessions, searchHistorySession,
// sortHistoryMatches, historySearchStatus and pageHistoryMatches. On two lineages (one with every transcript
// readable, one with an ancestor's file missing, one empty and the current transcript nowhere), random searches
// (queries with terms and quoted phrases, roles, ancestors on or off, a target in or outside the lineage, pages and
// excerpt lengths, a missing or unknown caller) get the same answer from the old function and the new one.
func TestSearchAgentHistoryAgreesWithBefore4834(t *testing.T) {
	env := setupAgentHistoryTestEnv(t)
	ctx := context.Background()
	work := filepath.Join(env.claudeHome, "oracle-project")
	require.NoError(t, os.MkdirAll(work, 0o755))
	proj := filepath.Join(env.claudeHome, strings.ReplaceAll(work, "/", "-"))
	require.NoError(t, os.MkdirAll(proj, 0o755))
	line := func(typ, ts, text string) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "timestamp": ts, "message": map[string]any{"content": text}})
		return string(b) + "\n"
	}
	write := func(id string, lines ...string) string {
		path := filepath.Join(proj, id+".jsonl")
		require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644))
		return path
	}
	index := func(id, file string) {
		ts := "2026-09-01T00:00:00Z"
		require.NoError(t, env.ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: id, SourceType: "claude", SourceFile: file, FirstTimestamp: &ts, LastTimestamp: &ts, MessageCount: 1}))
	}
	live := func(id, from string) {
		require.NoError(t, env.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: at.Claude, AgentName: "w-" + id[len(id)-2:], WorkingDir: work, ResumeFromID: ptr.NonEmpty(from)}))
	}
	// Lineage A, all readable: a0 (indexed file) <- a1 (stopped live) <- a2 (current).
	a0, a1, a2 := "48340000-0000-4000-8000-0000000000a0", "48340000-0000-4000-8000-0000000000a1", "48340000-0000-4000-8000-0000000000a2"
	index(a0, write(a0,
		line("user", "2026-09-30T08:00:00Z", "The deploy key is ALPHA-99 for the staging cluster."),
		line("assistant", "2026-09-30T08:00:05Z", "Noted: deploy key ALPHA-99, staging cluster ready."),
		line("system", "2026-09-30T08:00:06Z", "system note about the deploy"),
		line("assistant", "2026-09-30T10:00:15+00:00", "same instant as the current session's reply: deploy done"),
		line("user", "not a time", "an entry with no usable timestamp mentions the deploy")))
	write(a1,
		line("user", "2026-09-30T09:00:00Z", "Move the staging cluster to US-East please."),
		line("assistant", "2026-09-30T09:00:10.500Z", "The staging cluster runs in US-East now; deploy verified."))
	live(a1, a0)
	write(a2,
		line("user", "2026-09-30T10:00:00Z", "Ready for the production deploy?"),
		line("assistant", "2026-09-30T10:00:15Z", "Production deploy started, staging cluster untouched."),
		line("user", "2026-09-30T10:01:00Z", strings.Repeat("long text without the word ", 40)+"deploy at the end"))
	live(a2, a1)
	// Lineage B: b0's file is gone, b1's transcript is empty, b2 (current) has no transcript at all.
	b0, b1, b2 := "48340000-0000-4000-8000-0000000000b0", "48340000-0000-4000-8000-0000000000b1", "48340000-0000-4000-8000-0000000000b2"
	index(b0, filepath.Join(proj, "gone.jsonl"))
	index(b1, write(b1))
	live(b1, b0)
	live(b2, b1)
	// Lineage C: c1 (current, readable) resumes b0, whose file is gone: partial.
	c1 := "48340000-0000-4000-8000-0000000000c1"
	write(c1, line("assistant", "2026-10-01T12:00:00Z", "Continuing: the deploy key ALPHA-99 still works."))
	live(c1, b0)

	call := func(path string, q url.Values) (int, string) {
		resp, err := http.Get(env.server.URL + path + "?" + q.Encode())
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	queries := []string{"deploy", "ALPHA-99", "\"staging cluster\"", "deploy staging", "\"deploy key\" ALPHA-99", "US-East", "nothing-matches-this",
		"DEPLOY", "  ", "", strings.Repeat("x", 1001), "production \"staging cluster\""}
	callers := []string{a2, a2, a2, b2, b2, c1, "48340000-0000-4000-8000-0000000000ff", ""}
	targets := []string{"", "", "", a0, a1, a2, b0, b1, c1, "48340000-0000-4000-8000-0000000000ee"}
	counts := map[string]int{}
	for seed := int64(1); seed <= 1500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		q := url.Values{}
		key := []string{"query", "q"}[rng.Intn(2)]
		q.Set(key, queries[rng.Intn(len(queries))])
		if c := callers[rng.Intn(len(callers))]; c != "" {
			q.Set("session_id", c)
		}
		if rng.Intn(3) == 0 {
			q.Set("roles", []string{"user", "assistant", "assistant,user,system", "system", "bogus", "user, assistant"}[rng.Intn(6)])
		}
		if rng.Intn(3) == 0 {
			q.Set("include_ancestors", []string{"false", "0", "no", "true", "yes", ""}[rng.Intn(6)])
		}
		if tg := targets[rng.Intn(len(targets))]; tg != "" && rng.Intn(2) == 0 {
			q.Set("target_session_id", tg)
		}
		if rng.Intn(2) == 0 {
			q.Set("limit", []string{"1", "2", "3", "0", "-4", "500", "x"}[rng.Intn(7)])
		}
		if rng.Intn(2) == 0 {
			q.Set("offset", []string{"0", "1", "2", "5", "50", "-1"}[rng.Intn(6)])
		}
		if rng.Intn(3) == 0 {
			q.Set("max_excerpt_chars", []string{"10", "60", "300", "5000", "abc"}[rng.Intn(5)])
		}
		if rng.Intn(8) == 0 { // the long message, at the shortest excerpt asked for
			q = url.Values{"session_id": {a2}, key: {"deploy"}, "target_session_id": {a2}, "limit": {"100"}, "max_excerpt_chars": {"10"}}
		}
		oc, ob := call("/api/agent/history/search-old", q)
		nc, nb := call("/api/agent/history/search", q)
		require.Equal(t, oc, nc, "seed %d %s", seed, q.Encode())
		require.Equal(t, ob, nb, "seed %d %s", seed, q.Encode())

		var resp AgentHistorySearchResponse
		_ = json.Unmarshal([]byte(nb), &resp)
		switch {
		case strings.Contains(nb, "is required") && strings.Contains(nb, "query"):
			counts["no query"]++
		case strings.Contains(nb, "maximum length"):
			counts["query too long"]++
		case strings.Contains(nb, "session_id parameter is required"):
			counts["no session named"]++
		case strings.Contains(nb, "Unknown session"):
			counts["unknown session"]++
		case strings.Contains(nb, "not within the authenticated session lineage"):
			counts["target outside the lineage"]++
		}
		if nc != http.StatusOK {
			continue
		}
		switch resp.Status {
		case "complete":
			counts["complete: every session ready"]++
		case "partial":
			counts["partial: some transcript unavailable"]++
		case "unavailable":
			counts["unavailable: nothing readable"]++
		}
		for _, s := range resp.SessionsSearched {
			if s.Status == "empty" {
				counts["an empty transcript"]++
			}
		}
		if len(resp.SessionsSearched) == 1 && q.Get("target_session_id") != "" {
			counts["a target session searched alone"]++
		}
		if inc := q.Get("include_ancestors"); (inc == "false" || inc == "0" || inc == "no") && q.Get("target_session_id") == "" {
			counts["the current session only"]++
		}
		if len(resp.Results) == 0 && resp.TotalMatches > 0 {
			counts["a page past the end"]++
		}
		if resp.HasMore {
			counts["more after this page"]++
		}
		roles := map[string]bool{}
		for i, m := range resp.Results {
			roles[m.Role] = true
			if !m.IsCurrent {
				counts["an ancestor's match"]++
			}
			if strings.Contains(q.Get(key), "\"") && len(m.MatchTerms) > 0 {
				counts["a quoted phrase matched"]++
			}
			if len(m.MatchTerms) > 1 {
				counts["several terms matched"]++
			}
			if m.Timestamp == "not a time" {
				counts["an unparsable timestamp listed"]++
			}
			if i > 0 && resp.Results[i-1].IsCurrent && !m.IsCurrent && strings.HasPrefix(resp.Results[i-1].Timestamp, "2026-09-30T10:00:15") && strings.HasPrefix(m.Timestamp, "2026-09-30T10:00:15") {
				counts["current before ancestor at the same time"]++
			}
			if q.Get("max_excerpt_chars") == "10" && m.SessionID == a2 && m.MessageIndex == 2 { // the 1000-character message
				require.Less(t, len(m.Excerpt), 80, "clamped to 50 characters, not 10 and not the message")
				counts["the excerpt clamped short"]++
			}
		}
		if len(roles) == 1 && q.Get("roles") != "" {
			counts["a role filter left one role"]++
		}
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range historySearchOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

// searchAgentHistoryBefore4834 is SearchAgentHistory as it was before #4834.
func (h *SessionsHandler) searchAgentHistoryBefore4834(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	sessionID, ok := historyCaller(w, r, q)
	if !ok {
		return
	}

	searchQuery := strings.TrimSpace(q.Get("query"))
	if searchQuery == "" {
		searchQuery = strings.TrimSpace(q.Get("q"))
	}
	if searchQuery == "" {
		errBadRequest(w, "query parameter 'query' or 'q' is required")
		return
	}
	if len(searchQuery) > 1000 {
		errBadRequest(w, "query exceeds maximum length of 1000 characters")
		return
	}

	rolesParam := q.Get("roles")
	if rolesParam == "" {
		rolesParam = "assistant,user"
	}
	allowedRoles := parseRoles(rolesParam)

	includeAncestors := true
	if inc := q.Get("include_ancestors"); inc != "" {
		if inc == "false" || inc == "0" || inc == "no" {
			includeAncestors = false
		}
	}

	targetSessionID := strings.TrimSpace(q.Get("target_session_id"))

	limit := clampLimit(queryInt(r, "limit", 20), 20, 100)
	offset := clampOffset(queryInt(r, "offset", 0))

	maxExcerptChars := queryInt(r, "max_excerpt_chars", 300)
	if maxExcerptChars < 50 {
		maxExcerptChars = 50
	} else if maxExcerptChars > 1000 {
		maxExcerptChars = 1000
	}

	// Authenticate caller session and resolve proven resume ancestry
	ancestry, err := h.ss.GetSessionAncestry(r.Context(), sessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if len(ancestry) == 0 {
		errNotFound(w, "Unknown session; run this from inside a Coral agent")
		return
	}

	// Validate target_session_id if specified (must be in lineage)
	var sessionsToSearch []store.SessionLineageItem
	if targetSessionID != "" {
		var matched *store.SessionLineageItem
		for i := range ancestry {
			if ancestry[i].SessionID == targetSessionID {
				matched = &ancestry[i]
				break
			}
		}
		if matched == nil {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "Unauthorized: session is not within the authenticated session lineage",
			})
			return
		}
		sessionsToSearch = []store.SessionLineageItem{*matched}
	} else if !includeAncestors {
		sessionsToSearch = ancestry[:1]
	} else {
		sessionsToSearch = ancestry
	}

	phrases, terms := parseSearchQuery(searchQuery)

	var allMatches []AgentHistoryMatch
	var sessionsSearched []SessionSearchStatus

	for _, item := range sessionsToSearch {
		roleLabel := "ancestor"
		if item.IsCurrent {
			roleLabel = "current"
		}

		messages, errMsg, err := h.loadSessionMessages(r.Context(), item)
		if err != nil {
			sessionsSearched = append(sessionsSearched, SessionSearchStatus{
				SessionID:        item.SessionID,
				Role:             roleLabel,
				Status:           "transcript_unavailable",
				MessagesSearched: 0,
				Error:            err.Error(),
			})
			continue
		}
		if errMsg != "" {
			sessionsSearched = append(sessionsSearched, SessionSearchStatus{
				SessionID:        item.SessionID,
				Role:             roleLabel,
				Status:           "transcript_unavailable",
				MessagesSearched: 0,
				Error:            errMsg,
			})
			continue
		}
		if len(messages) == 0 {
			sessionsSearched = append(sessionsSearched, SessionSearchStatus{
				SessionID:        item.SessionID,
				Role:             roleLabel,
				Status:           "empty",
				MessagesSearched: 0,
			})
			continue
		}

		sessionsSearched = append(sessionsSearched, SessionSearchStatus{
			SessionID:        item.SessionID,
			Role:             roleLabel,
			Status:           "ready",
			MessagesSearched: len(messages),
		})

		for idx, msg := range messages {
			msgRole, _ := msg["type"].(string)
			if !allowedRoles[msgRole] {
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

			firstOffset := 0
			if len(offsets) > 0 {
				firstOffset = offsets[0][0]
			}
			excerpt := makeBoundedExcerpt(text, firstOffset, maxExcerptChars)
			timestamp, _ := msg["timestamp"].(string)

			allMatches = append(allMatches, AgentHistoryMatch{
				SessionID:    item.SessionID,
				IsCurrent:    item.IsCurrent,
				MessageIndex: idx,
				Role:         msgRole,
				Timestamp:    timestamp,
				Excerpt:      excerpt,
				MatchOffsets: offsets,
				MatchTerms:   matchedTerms,
			})
		}
	}

	// Sort matches descending by timestamp (newest first).
	sort.SliceStable(allMatches, func(i, j int) bool {
		ti, errI := parseFlexibleTimestamp(allMatches[i].Timestamp)
		tj, errJ := parseFlexibleTimestamp(allMatches[j].Timestamp)
		if errI == nil && errJ == nil && !ti.Equal(tj) {
			return ti.After(tj)
		}
		if allMatches[i].IsCurrent != allMatches[j].IsCurrent {
			return allMatches[i].IsCurrent
		}
		return allMatches[i].MessageIndex > allMatches[j].MessageIndex
	})

	// Determine overall status
	status := "complete"
	allReady := true
	anyReady := false
	for _, s := range sessionsSearched {
		if s.Status == "ready" {
			anyReady = true
		} else {
			allReady = false
		}
	}
	if !anyReady {
		status = "unavailable"
	} else if !allReady {
		status = "partial"
	}

	totalMatches := len(allMatches)
	start := min(offset, totalMatches)
	end := min(start+limit, totalMatches)

	results := allMatches[start:end]
	if results == nil {
		results = []AgentHistoryMatch{}
	}

	resp := AgentHistorySearchResponse{
		Query:            searchQuery,
		Status:           status,
		TotalMatches:     totalMatches,
		HasMore:          end < totalMatches,
		Limit:            limit,
		Offset:           offset,
		Results:          results,
		SessionsSearched: sessionsSearched,
	}

	writeJSON(w, http.StatusOK, resp)
}
