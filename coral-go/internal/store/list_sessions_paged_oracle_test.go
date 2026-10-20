package store

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// #4756: ListSessionsPaged split into sessionListQuery, pageSessions, the four lookups and sessionListExtras.item.
// On one seeded index (timestamps in several layouts, NULL names, notes and auto summaries, tags, several git
// snapshots per session, live sessions with and without display names) the old function and the new one return
// the same result for random filters, each condition below counted from it happening, with a floor.
func TestListSessionsPagedAgreesWithBefore4756(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(4756))
	lasts := []string{"2026-10-05T12:00:00Z", "2026-10-05T23:59:59.500Z", "2026-10-06T00:00:00+00:00", "2026-10-07T09:30:00.250000+00:00",
		"2026-10-09T18:00:00", "2026-10-12T08:00:00Z", "2026-10-15T12:00:00+00:00", "2026-10-15T12:00:00Z"}
	words := []string{"deploy", "build", "tests", "docs", "release", "flaky"}
	var tagIDs []int64
	for i, name := range []string{"zeta", "alpha", "mid"} {
		tag, err := s.CreateTag(ctx, name, fmt.Sprintf("#00000%d", i))
		require.NoError(t, err)
		tagIDs = append(tagIDs, tag.ID)
	}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("4756%04d-0000-4000-8000-%012d", i, i)
		last := lasts[rng.Intn(len(lasts))]
		first := last[:11] + "0" + fmt.Sprint(rng.Intn(9)) + ":00:00Z" // the same day, earlier (or the same hour)
		idx := &SessionIndex{SessionID: id, SourceType: []string{"claude", "codex", "agy"}[rng.Intn(3)], SourceFile: "/f/" + id,
			FirstTimestamp: &first, LastTimestamp: &last, MessageCount: rng.Intn(50), DisplaySummary: words[rng.Intn(len(words))] + " work",
			AgentName: []string{"", "lead", "qa"}[rng.Intn(3)], DisplayName: []string{"", "Lead Dev"}[rng.Intn(2)], FileMtime: 1}
		if i%9 == 0 {
			idx.FirstTimestamp = nil // no duration
		}
		require.NoError(t, s.UpsertSessionIndex(ctx, idx))
		if i%7 == 0 {
			_, err := db.ExecContext(ctx, "UPDATE session_index SET agent_name = NULL, display_name = NULL WHERE session_id = ?", id)
			require.NoError(t, err)
		}
		require.NoError(t, s.UpsertFTS(ctx, id, words[rng.Intn(len(words))]+" "+words[rng.Intn(len(words))]+" and more"))
		switch i % 4 {
		case 0:
			require.NoError(t, s.SaveSessionNotes(ctx, id, "# Notes title\nbody"))
		case 1:
			require.NoError(t, s.SaveAutoSummary(ctx, id, "## Auto title\nsummary"))
		case 2:
			require.NoError(t, s.SaveAutoSummary(ctx, id, "no header here"))
		}
		for _, tid := range tagIDs {
			if rng.Intn(3) == 0 {
				require.NoError(t, s.AddSessionTag(ctx, id, tid))
			}
		}
		for k := rng.Intn(3); k > 0; k-- {
			_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots(agent_name,agent_type,working_directory,branch,commit_hash,session_id,recorded_at) VALUES('repo','claude','/repo',?,?,?,?)`,
				fmt.Sprintf("branch-%d-%d", i, k), fmt.Sprintf("%d-%d", i, k), id, fmt.Sprintf("2026-10-0%dT00:00:00Z", k))
			require.NoError(t, err)
		}
		if i%5 == 0 {
			ls := &LiveSession{SessionID: id, AgentName: []string{"live-agent", ""}[i%2], AgentType: "claude", WorkingDir: "/w"}
			if i%10 == 0 {
				dn := "Live Name"
				ls.DisplayName = &dn
			}
			require.NoError(t, s.RegisterLiveSession(ctx, ls))
		}
	}

	counts := map[string]int{}
	for seed := int64(1); seed <= 1500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		p := SessionListParams{Page: []int{0, 1, 1, 2, 3, 9}[rng.Intn(6)], PageSize: []int{0, 5, 10, 50, -1}[rng.Intn(5)]}
		if rng.Intn(3) == 0 {
			p.Search = []string{"deploy", "build tests", "\"release\"", "nothing", "flaky docs", "*"}[rng.Intn(6)]
			p.FTSMode = []string{"", "and", "or", "phrase"}[rng.Intn(4)]
		}
		if rng.Intn(3) == 0 {
			p.DateFrom = []string{"2026-10-05", "2026-10-06", "2026-10-10", "2026-11-01"}[rng.Intn(4)]
		}
		if rng.Intn(3) == 0 {
			p.DateTo = []string{"2026-10-05", "2026-10-07", "2026-10-15", "2026-01-01"}[rng.Intn(4)]
		}
		if rng.Intn(5) == 0 {
			v := []int{0, 3600, 20000}[rng.Intn(3)]
			p.MinDurationSec = &v
		}
		if rng.Intn(5) == 0 {
			v := []int{0, 7200, 40000}[rng.Intn(3)]
			p.MaxDurationSec = &v
		}
		if rng.Intn(3) == 0 {
			for _, tid := range tagIDs {
				if rng.Intn(2) == 0 {
					p.TagIDs = append(p.TagIDs, tid)
				}
			}
			p.TagLogic = []string{"AND", "OR", ""}[rng.Intn(3)]
		}
		if rng.Intn(4) == 0 {
			p.SourceTypes = [][]string{{"claude"}, {"codex", "agy"}, {"none"}}[rng.Intn(3)]
		}
		if rng.Intn(5) == 0 {
			p.AgentName = []string{"lead", "qa", "live-agent", "nobody"}[rng.Intn(4)]
		}
		want, werr := s.listSessionsPagedBefore4756(ctx, p)
		got, gerr := s.ListSessionsPaged(ctx, p)
		require.Equal(t, werr, gerr, "seed %d %+v", seed, p)
		require.Equal(t, want, got, "seed %d %+v", seed, p)
		if got == nil {
			continue
		}

		all, err := s.listSessionsPagedBefore4756(ctx, SessionListParams{Page: 1, PageSize: 1000})
		require.NoError(t, err)
		switch {
		case p.Search != "" && len(got.Sessions) > 1:
			counts["a search, ranked"]++
		case len(got.Sessions) == 0 && got.Total > 0:
			counts["a page past the end"]++
		case got.Page > 1 && len(got.Sessions) > 0:
			counts["a later page"]++
		}
		if got.Total < all.Total && p.Search == "" && len(p.TagIDs) == 0 && len(p.SourceTypes) == 0 && p.AgentName == "" && p.MinDurationSec == nil && p.MaxDurationSec == nil && (p.DateFrom != "" || p.DateTo != "") {
			counts["dates excluded sessions"]++
		}
		for _, it := range got.Sessions {
			if p.DateTo == "2026-10-05" && it.LastTimestamp != nil && *it.LastTimestamp == "2026-10-05T23:59:59.500Z" {
				counts["a session ending 23:59:59.5 on the date_to day"]++
			}
			if len(it.Tags) > 1 {
				counts["several tags, by name"]++
				require.LessOrEqual(t, it.Tags[0].Name, it.Tags[1].Name)
			}
			if len(it.Tags) == 0 {
				counts["no tags (an empty list)"]++
				require.NotNil(t, it.Tags)
			}
			if it.Branch != nil {
				counts["a latest branch"]++
			}
			if it.SummaryTitle == "Notes title" {
				counts["title from notes"]++
			}
			if it.SummaryTitle == "Auto title" {
				counts["title from an auto summary"]++
			}
			if it.HasNotes && it.SummaryTitle == "" {
				counts["notes without a header"]++
			}
			if it.AgentName == "live-agent" {
				counts["live agent name over the index's"]++
			}
			if it.DisplayName == "Live Name" {
				counts["live display name over the index's"]++
			}
			if it.DurationSec == nil {
				counts["no duration"]++
			}
		}
		if len(p.TagIDs) > 1 && p.TagLogic == "AND" && len(got.Sessions) > 0 {
			counts["all of several tags"]++
		}
		if len(p.TagIDs) > 1 && p.TagLogic != "AND" && len(got.Sessions) > 0 {
			counts["any of several tags"]++
		}
		if (p.MinDurationSec != nil || p.MaxDurationSec != nil) && got.Total < all.Total && got.Total > 0 {
			counts["a duration filter excluded some"]++
		}
		if len(p.SourceTypes) > 1 && len(got.Sessions) > 0 {
			counts["several source types"]++
		}
		if p.AgentName != "" && got.Total > 0 {
			counts["an agent name filter"]++
		}
	}
	t.Logf("compared, per condition: %v", counts)
	for _, k := range []string{"a search, ranked", "a page past the end", "a later page", "dates excluded sessions",
		"a session ending 23:59:59.5 on the date_to day", "several tags, by name", "no tags (an empty list)", "a latest branch",
		"title from notes", "title from an auto summary", "notes without a header", "live agent name over the index's",
		"live display name over the index's", "no duration", "all of several tags", "any of several tags",
		"a duration filter excluded some", "several source types", "an agent name filter"} {
		require.GreaterOrEqual(t, counts[k], 10, "%q went (almost) unexercised", k)
	}
}

// listSessionsPagedBefore4756 is ListSessionsPaged as it was before #4756 (with #4779's date_to fix).
func (s *SessionStore) listSessionsPagedBefore4756(ctx context.Context, params SessionListParams) (*SessionListResult, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 50
	}
	if params.FTSMode == "" {
		// Natural-language searches should return the best partial matches instead
		// of disappearing as soon as one query word is absent. FTS rank still puts
		// sessions matching more (and rarer) terms first; callers can explicitly
		// request "and" when every word is required.
		params.FTSMode = "or"
	}

	var args []interface{}
	var whereClauses []string
	fromClause := "session_index si"
	orderClause := "si.last_timestamp DESC"

	// Full-text search
	if params.Search != "" {
		safeQ := sanitizeFTSQuery(params.Search, params.FTSMode)
		if safeQ != "" {
			fromClause += " JOIN session_fts fts ON fts.session_id = si.session_id"
			whereClauses = append(whereClauses, "session_fts MATCH ?")
			args = append(args, safeQ)
			orderClause = "rank"
		}
	}

	// Date filters
	if params.DateFrom != "" {
		whereClauses = append(whereClauses, "si.last_timestamp >= ?")
		args = append(args, params.DateFrom+"T00:00:00")
	}
	if params.DateTo != "" {
		// Through the end of the date_to day, fractions of a second included: "~" sorts after every character of a timestamp, so
		// "< date~" is every timestamp that begins with a date up to date_to (a session ending at 23:59:59.5 is not after "T23:59:59").
		whereClauses = append(whereClauses, "si.last_timestamp < ?")
		args = append(args, params.DateTo+"~")
	}

	// Duration filters
	if params.MinDurationSec != nil {
		whereClauses = append(whereClauses,
			"(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 >= ?")
		args = append(args, *params.MinDurationSec)
	}
	if params.MaxDurationSec != nil {
		whereClauses = append(whereClauses,
			"(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 <= ?")
		args = append(args, *params.MaxDurationSec)
	}

	// Tag filters
	if len(params.TagIDs) > 0 && params.TagLogic == "AND" {
		for _, tid := range params.TagIDs {
			whereClauses = append(whereClauses,
				"si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id = ?)")
			args = append(args, tid)
		}
	} else if len(params.TagIDs) > 0 {
		whereClauses = append(whereClauses,
			"si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id IN (?))")
		args = append(args, params.TagIDs)
	}

	// Source type filter
	if len(params.SourceTypes) > 0 {
		whereClauses = append(whereClauses, "si.source_type IN (?)")
		args = append(args, params.SourceTypes)
	}

	// Agent name filter
	if params.AgentName != "" {
		whereClauses = append(whereClauses, "si.agent_name = ?")
		args = append(args, params.AgentName)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", fromClause, whereSQL)
	countSQL, countArgs, err := sqlx.In(countSQL, args...)
	if err != nil {
		return nil, err
	}
	var total int
	if err := s.reader().GetContext(ctx, &total, countSQL, countArgs...); err != nil {
		return nil, err
	}

	// Fetch page
	offset := (params.Page - 1) * params.PageSize
	selectFields := `si.session_id, si.source_type, si.source_file,
		si.first_timestamp, si.last_timestamp, si.message_count, si.display_summary,
		si.agent_name, si.display_name`
	query := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT ? OFFSET ?",
		selectFields, fromClause, whereSQL, orderClause)
	query, pageArgs, err := sqlx.In(query, append(args, params.PageSize, offset)...)
	if err != nil {
		return nil, err
	}

	var rows []struct {
		SessionID      string  `db:"session_id"`
		SourceType     string  `db:"source_type"`
		SourceFile     string  `db:"source_file"`
		FirstTimestamp *string `db:"first_timestamp"`
		LastTimestamp  *string `db:"last_timestamp"`
		MessageCount   int     `db:"message_count"`
		DisplaySummary string  `db:"display_summary"`
		AgentName      *string `db:"agent_name"`
		DisplayName    *string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, pageArgs...); err != nil {
		return nil, err
	}

	// Collect session IDs for enrichment
	sessionIDs := make([]string, len(rows))
	for i, r := range rows {
		sessionIDs[i] = r.SessionID
	}

	// Enrich with metadata
	metaMap := make(map[string]struct {
		HasNotes     bool
		IsUserEdited bool
		SummaryTitle string
	})
	tagsMap := make(map[string][]Tag)
	branchMap := make(map[string]string)
	agentMap := make(map[string]struct {
		AgentName   string
		DisplayName string
	})

	if len(sessionIDs) > 0 {
		// Fetch notes metadata
		metaQuery, metaArgs, _ := sqlx.In(
			"SELECT session_id, notes_md, auto_summary, is_user_edited FROM session_meta WHERE session_id IN (?)",
			sessionIDs)
		var metaRows []struct {
			SessionID    string `db:"session_id"`
			NotesMD      string `db:"notes_md"`
			AutoSummary  string `db:"auto_summary"`
			IsUserEdited bool   `db:"is_user_edited"`
		}
		if err := s.reader().SelectContext(ctx, &metaRows, metaQuery, metaArgs...); err == nil {
			for _, r := range metaRows {
				content := r.NotesMD
				if content == "" {
					content = r.AutoSummary
				}
				metaMap[r.SessionID] = struct {
					HasNotes     bool
					IsUserEdited bool
					SummaryTitle string
				}{
					HasNotes:     r.NotesMD != "" || r.AutoSummary != "",
					IsUserEdited: r.IsUserEdited,
					SummaryTitle: extractFirstHeader(content),
				}
			}
		}

		// Fetch tags
		tagQuery, tagArgs, _ := sqlx.In(
			`SELECT st.session_id, t.id, t.name, t.color
			 FROM session_tags st JOIN tags t ON t.id = st.tag_id
			 WHERE st.session_id IN (?) ORDER BY t.name`,
			sessionIDs)
		var tagRows []struct {
			SessionID string `db:"session_id"`
			ID        int64  `db:"id"`
			Name      string `db:"name"`
			Color     string `db:"color"`
		}
		if err := s.reader().SelectContext(ctx, &tagRows, tagQuery, tagArgs...); err == nil {
			for _, r := range tagRows {
				tagsMap[r.SessionID] = append(tagsMap[r.SessionID], Tag{
					ID: r.ID, Name: r.Name, Color: r.Color,
				})
			}
		}

		// Fetch git branches
		branchQuery, branchArgs, _ := sqlx.In(
			`SELECT gs.session_id, gs.branch
			 FROM git_snapshots gs
			 INNER JOIN (
			     SELECT session_id, MAX(recorded_at) as max_ts
			     FROM git_snapshots WHERE session_id IN (?)
			     GROUP BY session_id
			 ) latest ON gs.session_id = latest.session_id AND gs.recorded_at = latest.max_ts`,
			sessionIDs)
		var branchRows []struct {
			SessionID string `db:"session_id"`
			Branch    string `db:"branch"`
		}
		if err := s.reader().SelectContext(ctx, &branchRows, branchQuery, branchArgs...); err == nil {
			for _, r := range branchRows {
				branchMap[r.SessionID] = r.Branch
			}
		}

		// Fetch agent names from live_sessions
		agentQuery, agentArgs, _ := sqlx.In(
			`SELECT session_id, agent_name, display_name FROM live_sessions WHERE session_id IN (?)`,
			sessionIDs)
		var agentRows []struct {
			SessionID   string  `db:"session_id"`
			AgentName   string  `db:"agent_name"`
			DisplayName *string `db:"display_name"`
		}
		if err := s.reader().SelectContext(ctx, &agentRows, agentQuery, agentArgs...); err == nil {
			for _, r := range agentRows {
				agentMap[r.SessionID] = struct {
					AgentName   string
					DisplayName string
				}{
					AgentName: r.AgentName,
				}
				if r.DisplayName != nil {
					entry := agentMap[r.SessionID]
					entry.DisplayName = *r.DisplayName
					agentMap[r.SessionID] = entry
				}
			}
		}
	}

	// Build results
	sessions := make([]SessionListItem, len(rows))
	for i, r := range rows {
		meta := metaMap[r.SessionID]
		tags := tagsMap[r.SessionID]
		if tags == nil {
			tags = []Tag{}
		}
		var branch *string
		if b, ok := branchMap[r.SessionID]; ok {
			branch = &b
		}

		// Agent name: use session_index as primary, live_sessions as override
		agentName := ""
		displayName := ""
		if r.AgentName != nil && *r.AgentName != "" {
			agentName = *r.AgentName
		}
		if r.DisplayName != nil && *r.DisplayName != "" {
			displayName = *r.DisplayName
		}
		// Override with live_sessions data if available (more current for active agents)
		if info, ok := agentMap[r.SessionID]; ok {
			if info.AgentName != "" {
				agentName = info.AgentName
			}
			if info.DisplayName != "" {
				displayName = info.DisplayName
			}
		}

		sessions[i] = SessionListItem{
			SessionID:      r.SessionID,
			SourceType:     r.SourceType,
			SourceFile:     r.SourceFile,
			FirstTimestamp: r.FirstTimestamp,
			LastTimestamp:  r.LastTimestamp,
			MessageCount:   r.MessageCount,
			Summary:        r.DisplaySummary,
			SummaryTitle:   meta.SummaryTitle,
			HasNotes:       meta.HasNotes,
			Tags:           tags,
			Branch:         branch,
			DurationSec:    computeDuration(r.FirstTimestamp, r.LastTimestamp),
			AgentName:      agentName,
			DisplayName:    displayName,
		}
	}

	return &SessionListResult{
		Sessions: sessions,
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
	}, nil
}
