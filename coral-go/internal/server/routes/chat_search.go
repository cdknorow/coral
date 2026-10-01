package routes

import (
	"net/http"
	"sort"
	"strings"

	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/store"
)

// ChatSearchHit is a bounded, plain-text match. MatchOffsets are byte offsets
// in Excerpt, so clients can safely highlight without rendering HTML.
type ChatSearchHit struct {
	MessageIndex int         `json:"message_index,omitempty"`
	MessageID    int64       `json:"message_id,omitempty"`
	Role         string      `json:"role"`
	Timestamp    string      `json:"timestamp,omitempty"`
	Excerpt      string      `json:"excerpt"`
	MatchOffsets [][]int     `json:"match_offsets,omitempty"`
	Locator      ChatLocator `json:"locator"`
}

type ChatLocator struct {
	SessionID    string `json:"session_id,omitempty"`
	Project      string `json:"project,omitempty"`
	MessageID    int64  `json:"message_id,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
}

type ChatSearchResult struct {
	SessionID     string          `json:"session_id"`
	Type          string          `json:"type"`
	SourceType    string          `json:"source_type"`
	Score         float64         `json:"score"`
	LastTimestamp string          `json:"last_timestamp,omitempty"`
	Hits          []ChatSearchHit `json:"hits"`
}

// SearchChats searches indexed agent transcripts and board messages. The
// history index is used to bound transcript reads; unavailable files are
// reported honestly through status rather than triggering an unbounded scan.
// GET /api/sessions/history/search?q=...&type=all&page=1&page_size=20
func (h *HistoryHandler) SearchChats(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 1000 {
		errBadRequest(w, "q is required and must be at most 1000 characters")
		return
	}
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryInt(r, "page_size", 20)
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	chatType := r.URL.Query().Get("type")
	if chatType != "agent" && chatType != "group" {
		chatType = "all"
	}

	phrases, terms := parseSearchQuery(q)
	results := make([]ChatSearchResult, 0)
	status := "complete"
	unavailable := 0

	if chatType == "all" || chatType == "agent" {
		// Read at most 200 indexed sessions per request. FTS remains the
		// shortlist; message parsing is performed only for those sessions.
		listed, err := h.ss.ListSessionsPaged(r.Context(), store.SessionListParams{Page: 1, PageSize: 200, Search: q, FTSMode: "or"})
		if err != nil {
			status = "unavailable"
		} else {
			for _, idx := range listed.Sessions {
				if idx.SourceFile == "" || !fileExists(idx.SourceFile) {
					unavailable++
					continue
				}
				messages, _, err := jsonl.ReadFrom(idx.SourceFile, idx.SourceType, 0)
				if err != nil {
					unavailable++
					continue
				}
				hits := make([]ChatSearchHit, 0, 4)
				for i, msg := range messages {
					text := cleanWhitespace(extractSearchableMessageText(msg))
					matched, offsets, _ := matchMessage(text, phrases, terms)
					if !matched {
						continue
					}
					excerpt, excerptOffsets := searchExcerpt(text, offsets, 320)
					excerptOffsets = utf16Offsets(excerpt, excerptOffsets)
					timestamp, _ := msg["timestamp"].(string)
					role, _ := msg["type"].(string)
					hits = append(hits, ChatSearchHit{MessageIndex: i, Role: role, Timestamp: timestamp, Excerpt: excerpt, MatchOffsets: excerptOffsets, Locator: ChatLocator{SessionID: idx.SessionID, MessageIndex: i}})
					if len(hits) >= 20 {
						break
					}
				}
				if len(hits) > 0 {
					score := float64(len(hits))
					results = append(results, ChatSearchResult{SessionID: idx.SessionID, Type: "agent", SourceType: idx.SourceType, Score: score, LastTimestamp: derefStrPtr(idx.LastTimestamp), Hits: hits})
				}
			}
		}
	}
	if chatType == "all" || chatType == "group" {
		if h.bs != nil {
			msgs, err := h.bs.SearchMessageHits(r.Context(), q, 500)
			if err != nil {
				status = "unavailable"
			} else {
				byProject := map[string]*ChatSearchResult{}
				for _, m := range msgs {
					content := cleanWhitespace(m.Content)
					off := strings.Index(strings.ToLower(content), strings.ToLower(q))
					if off < 0 {
						continue
					}
					excerpt, offsets := searchExcerpt(content, [][]int{{off, off + len(q)}}, 320)
					offsets = utf16Offsets(excerpt, offsets)
					res := byProject[m.Project]
					if res == nil {
						res = &ChatSearchResult{SessionID: "board:" + m.Project, Type: "group", SourceType: "board", Score: 1, LastTimestamp: m.CreatedAt}
						byProject[m.Project] = res
					}
					res.Hits = append(res.Hits, ChatSearchHit{MessageID: m.ID, Role: "board", Timestamp: m.CreatedAt, Excerpt: excerpt, MatchOffsets: offsets, Locator: ChatLocator{Project: m.Project, MessageID: m.ID}})
				}
				for _, res := range byProject {
					res.Score = float64(len(res.Hits))
					results = append(results, *res)
				}
			}
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].LastTimestamp > results[j].LastTimestamp
	})
	total := len(results)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	if unavailable > 0 && status == "complete" {
		status = "partial"
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "status": status, "total": total, "page": page, "page_size": pageSize, "has_more": end < total, "results": results[start:end], "match_offset_unit": "utf16_code_units", "index_coverage": map[string]any{"unavailable_sessions": unavailable, "transcript_shortlist_capped": true, "board_hit_cap": 500}})
}

func searchExcerpt(text string, offsets [][]int, max int) (string, [][]int) {
	text = cleanWhitespace(text)
	if len(text) <= max {
		return text, offsets
	}
	start := offsets[0][0] - max/3
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(text) {
		end = len(text)
		start = end - max
	}
	out := text[start:end]
	adjusted := make([][]int, 0, len(offsets))
	for _, o := range offsets {
		a, b := o[0]-start, o[1]-start
		if b > 0 && a < len(out) {
			if a < 0 {
				a = 0
			}
			if b > len(out) {
				b = len(out)
			}
			adjusted = append(adjusted, []int{a, b})
		}
	}
	return out, adjusted
}

// utf16Offsets makes offsets directly usable with JavaScript String indexes,
// including messages containing emoji or other supplementary Unicode planes.
func utf16Offsets(text string, offsets [][]int) [][]int {
	units := func(byteOffset int) int {
		return len([]rune(text[:byteOffset])) + countSupplementary(text[:byteOffset])
	}
	out := make([][]int, 0, len(offsets))
	for _, o := range offsets {
		if len(o) < 2 || o[0] < 0 || o[1] > len(text) {
			continue
		}
		out = append(out, []int{units(o[0]), units(o[1])})
	}
	return out
}

func countSupplementary(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xffff {
			n++
		}
	}
	return n
}
