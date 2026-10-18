package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/store"
)

// ListSessions, split into the query's reading, the agent and group-chat rows, the merge and the paging (#4713),
// answers every query as listSessionsBefore4713 (the handler at v1.4.92, below) does. One store holds agent sessions
// of every source type (agy and Gemini share history), tagged and untagged, with Z and +00:00 timestamps, and boards
// with messages; another is empty (the cold-start answer). Random queries (pages, sizes, tags, source types,
// durations, searches, dates, chat types, agent names; well-formed or not) go to both handlers and their responses
// are compared. Each kind of answer has a floor.

func historyListQuery(rng *rand.Rand) url.Values {
	q := url.Values{}
	set := func(k string, vals ...string) {
		if rng.Intn(3) == 0 {
			q.Set(k, vals[rng.Intn(len(vals))])
		}
	}
	set("page", "1", "2", "0", "-1", "x", "50")
	set("page_size", "1", "2", "5", "0", "201", "200", "x")
	set("tag_ids", "1", "1,2", " 2 , x", "999")
	set("source_types", "agy", "gemini", "antigravity", "codex", "claude,claude", " agy , gemini ,", "codex,agy")
	set("min_duration_sec", "0", "60", "x", "-5")
	set("max_duration_sec", "0", "3600", "x")
	set("q", "deploy", "team", "Team", "zzz", "fix")
	today := time.Now().UTC().Format("2006-01-02") // the boards' messages are stamped today: the bounds' own day
	set("date_from", "2026-10-01", "2026-10-10", "2026-12-01", today, today)
	set("date_to", "2026-10-05", "2026-10-20", "2025-01-01", today, today)
	set("type", "all", "agent", "group", "bogus")
	set("agent_name", "lead", "qa", "nobody")
	set("tag_logic", "and", "or")
	return q
}

func historyListOracleServer(t *testing.T, seed bool) *httptest.Server {
	_, h, ss, _, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()
	if seed {
		t1, err := ss.CreateTag(ctx, "urgent", "#f00")
		require.NoError(t, err)
		t2, err := ss.CreateTag(ctx, "docs", "#00f")
		require.NoError(t, err)
		types := []string{"claude", "codex", "agy", "gemini", "antigravity"}
		for i := 0; i < 24; i++ {
			sid := fmt.Sprintf("%08x-4713-4000-8000-%012x", i, i)
			zone := []string{"Z", "+00:00"}[i%2]
			first := fmt.Sprintf("2026-10-%02dT09:00:00%s", 1+i%20, zone)
			last := fmt.Sprintf("2026-10-%02dT%02d:30:00%s", 1+i%20, 9+i%6, zone)
			require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: types[i%len(types)], SourceFile: "/tmp/none-" + sid,
				FirstTimestamp: &first, LastTimestamp: &last, MessageCount: i * 3, DisplaySummary: []string{"deploy the build", "fix the tests", "write docs"}[i%3],
				AgentName: []string{"lead", "qa", ""}[i%3]}))
			if i%4 == 0 {
				require.NoError(t, ss.AddSessionTag(ctx, sid, t1.ID))
			}
			if i%5 == 0 {
				require.NoError(t, ss.AddSessionTag(ctx, sid, t2.ID))
			}
		}
		for i, project := range []string{"team-alpha", "deploy-crew", "quiet"} {
			for k := 0; k <= i; k++ {
				_, err := h.bs.Subscribe(ctx, project, fmt.Sprintf("member-%d", k), "Developer", fmt.Sprintf("s-%s-%d", project, k), nil, "all")
				require.NoError(t, err)
				_, err = h.bs.PostMessage(ctx, project, fmt.Sprintf("member-%d", k), []string{"let's deploy", "fix it", "hello team"}[k%3], nil)
				require.NoError(t, err)
			}
		}
	}
	r := chi.NewRouter()
	r.Get("/old", h.listSessionsBefore4713)
	r.Get("/new", h.ListSessions)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func historyListGet(t *testing.T, srv *httptest.Server, path string, q url.Values) (int, string) {
	resp, err := http.Get(srv.URL + path + "?" + q.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestListSessionsAgreesWithBefore4713(t *testing.T) {
	kinds := map[string]int{}
	for _, seeded := range []bool{true, false} {
		srv := historyListOracleServer(t, seeded)
		for seed := int64(1); seed <= 400; seed++ {
			rng := rand.New(rand.NewSource(seed))
			q := historyListQuery(rng)
			oc, ob := historyListGet(t, srv, "/old", q)
			nc, nb := historyListGet(t, srv, "/new", q)
			require.Equal(t, oc, nc, "seed %d %s", seed, q.Encode())
			require.Equal(t, ob, nb, "seed %d %s", seed, q.Encode())
			var out struct {
				Sessions []map[string]any `json:"sessions"`
				Total    int              `json:"total"`
				PageSize int              `json:"page_size"`
			}
			json.Unmarshal([]byte(nb), &out)
			typ := q.Get("type")
			if typ != "agent" && typ != "group" {
				typ = "all"
			}
			switch {
			case out.PageSize == 0:
				kinds["cold start"]++
			case len(out.Sessions) == 0:
				kinds["empty page ("+typ+")"]++
			default:
				kinds["rows ("+typ+")"]++
			}
			groups, agents := 0, 0
			for _, s := range out.Sessions {
				if s["type"] == "group" {
					groups++
				} else {
					agents++
				}
			}
			if groups > 0 && agents > 0 {
				kinds["merged agent and group rows"]++
			}
			if out.Total > len(out.Sessions) && len(out.Sessions) > 0 {
				kinds["a page of more"]++
			}
		}
	}
	t.Logf("compared, per kind: %v", kinds)
	for _, k := range []string{"cold start", "rows (all)", "rows (agent)", "rows (group)", "empty page (all)", "empty page (agent)", "empty page (group)",
		"merged agent and group rows", "a page of more"} {
		assert.GreaterOrEqual(t, kinds[k], 10, "%q went (almost) unexercised", k)
	}
}

// listSessionsBefore4713 is ListSessions before #4713 split it (history.go at v1.4.92), kept to compare with.
func (h *HistoryHandler) listSessionsBefore4713(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	var tagIDs []int64
	if raw := q.Get("tag_ids"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				tagIDs = append(tagIDs, id)
			}
		}
	}

	var sourceTypes []string
	if raw := q.Get("source_types"); raw != "" {
		seen := make(map[string]bool)
		for _, s := range strings.Split(raw, ",") {
			st := strings.TrimSpace(s)
			if st == "" || seen[st] {
				continue
			}
			seen[st] = true
			sourceTypes = append(sourceTypes, st)
			if (st == "agy" || st == "antigravity") && !seen["gemini"] {
				seen["gemini"] = true
				sourceTypes = append(sourceTypes, "gemini")
			} else if st == "gemini" && !seen["agy"] {
				seen["agy"] = true
				sourceTypes = append(sourceTypes, "agy")
			}
		}
	}

	var minDur, maxDur *int
	if v := q.Get("min_duration_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			minDur = &n
		}
	}
	if v := q.Get("max_duration_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxDur = &n
		}
	}

	params := store.SessionListParams{
		Page:           page,
		PageSize:       pageSize,
		Search:         q.Get("q"),
		FTSMode:        q.Get("fts_mode"),
		TagIDs:         tagIDs,
		TagLogic:       q.Get("tag_logic"),
		SourceTypes:    sourceTypes,
		DateFrom:       q.Get("date_from"),
		DateTo:         q.Get("date_to"),
		MinDurationSec: minDur,
		MaxDurationSec: maxDur,
		AgentName:      q.Get("agent_name"),
	}

	// Chat type filter: all, agent, group
	chatType := q.Get("type")
	if chatType != "agent" && chatType != "group" {
		chatType = "all"
	}

	// Check if any filters are active (used for cold-start fast path)
	hasAnyFilter := params.Search != "" || len(params.TagIDs) > 0 ||
		len(params.SourceTypes) > 0 || params.DateFrom != "" || params.DateTo != "" ||
		params.MinDurationSec != nil || params.MaxDurationSec != nil ||
		params.AgentName != ""

	// ── Agent sessions ──
	agentSessions := make([]map[string]any, 0)
	agentTotal := 0
	if chatType == "all" || chatType == "agent" {
		result, err := h.ss.ListSessionsPaged(r.Context(), params)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}

		// Cold-start fast path: when DB is empty and no filters active,
		// match Python's file-scan fallback response shape (page_size=0).
		if result.Total == 0 && !hasAnyFilter && chatType != "group" {
			writeJSON(w, http.StatusOK, map[string]any{
				"sessions":  agentSessions,
				"total":     0,
				"page":      1,
				"page_size": 0,
			})
			return
		}

		agentTotal = result.Total
		for _, s := range result.Sessions {
			m := map[string]any{
				"session_id":      s.SessionID,
				"source_type":     s.SourceType,
				"first_timestamp": ptr.Value(s.FirstTimestamp),
				"last_timestamp":  ptr.Value(s.LastTimestamp),
				"message_count":   s.MessageCount,
				"summary":         s.Summary,
				"summary_title":   s.SummaryTitle,
				"has_notes":       s.HasNotes,
				"tags":            s.Tags,
				"branch":          s.Branch,
				"duration_sec":    s.DurationSec,
				"agent_name":      s.AgentName,
				"display_name":    s.DisplayName,
				"type":            "agent",
			}
			agentSessions = append(agentSessions, m)
		}
	}

	// ── Group chats (board projects) ──
	var boardSessions []map[string]any
	if (chatType == "all" || chatType == "group") && h.bs != nil {
		projects, err := h.bs.ListProjectsEnriched(r.Context())
		if err == nil {
			searchQ := q.Get("q")
			dateFrom := q.Get("date_from")
			dateTo := q.Get("date_to")

			// Filter by text search
			if searchQ != "" {
				matchingProjects, _ := h.bs.SearchMessages(r.Context(), searchQ)
				matchSet := make(map[string]bool, len(matchingProjects))
				for _, p := range matchingProjects {
					matchSet[p] = true
				}
				qLower := strings.ToLower(searchQ)
				filtered := projects[:0]
				for _, p := range projects {
					if matchSet[p.Project] || strings.Contains(strings.ToLower(p.Project), qLower) {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}

			// Filter by date
			if dateFrom != "" {
				filtered := projects[:0]
				for _, p := range projects {
					if p.LastMessageAt != nil && *p.LastMessageAt >= dateFrom {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}
			if dateTo != "" {
				cutoff := dateTo + "T23:59:59"
				filtered := projects[:0]
				for _, p := range projects {
					if p.FirstMessageAt != nil && *p.FirstMessageAt <= cutoff {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}

			for _, p := range projects {
				participants := ""
				if p.ParticipantNames != nil {
					participants = *p.ParticipantNames
				}
				boardSessions = append(boardSessions, map[string]any{
					"session_id":        fmt.Sprintf("board:%s", p.Project),
					"title":             p.Project,
					"type":              "group",
					"source_type":       "board",
					"summary":           fmt.Sprintf("%d messages, %d participants", p.MessageCount, p.SubscriberCount),
					"first_timestamp":   ptr.Value(p.FirstMessageAt),
					"last_timestamp":    ptr.Value(p.LastMessageAt),
					"message_count":     p.MessageCount,
					"subscriber_count":  p.SubscriberCount,
					"participant_names": participants,
					"tags":              []any{},
					"has_notes":         false,
				})
			}
		}
	}

	// ── Merge and return ──
	if chatType == "agent" {
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions":  agentSessions,
			"total":     agentTotal,
			"page":      page,
			"page_size": pageSize,
		})
		return
	}

	if chatType == "group" {
		total := len(boardSessions)
		start := (page - 1) * pageSize
		end := start + pageSize
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions":  boardSessions[start:end],
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		})
		return
	}

	// type == "all": merge agent + board, sorted by last_timestamp desc
	merged := append(agentSessions, boardSessions...)
	sort.Slice(merged, func(i, j int) bool {
		ti, _ := merged[i]["last_timestamp"].(string)
		tj, _ := merged[j]["last_timestamp"].(string)
		// Normalize Z → +00:00 for consistent string comparison
		ti = strings.Replace(ti, "Z", "+00:00", 1)
		tj = strings.Replace(tj, "Z", "+00:00", 1)
		return ti > tj
	})
	total := agentTotal + len(boardSessions)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(merged) {
		start = len(merged)
	}
	if end > len(merged) {
		end = len(merged)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":  merged[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
