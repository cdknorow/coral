// Who a message mentions: mention terms, numbered names and orchestrator detection (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
)

// CheckUnread returns the count of unread messages based on the subscriber's receive_mode.
//
// Modes:
//   - "none"     → always 0
//   - "all"      → all unread messages from others
//   - "mentions" → only messages with @notify-all, @<subscriber_id>, or @<job_title>
//   - anything else → treat as group-id, count only messages from group members
//
// mentionTerms returns the canonical list of mention patterns for a subscriber.
// Used by CheckUnread (SQL LIKE), GetAllUnreadCounts (Go string matching), and ReadMessages.
func mentionTerms(subscriberID, jobTitle string) []string {
	return mentionTermsOf(subscriberID, jobTitle).terms
}

// termSet is a subscriber's mention terms and their lowercase forms. Shared between callers
// (memoized): they only read it.
type termSet struct {
	terms []string
	lower []string
}

var termMemo struct {
	sync.Mutex
	m map[[2]string]termSet
}

// termMemoLimit bounds the memo: a board has tens of subscribers, so this is generous; the map is
// dropped when full.
const termMemoLimit = 4096

// mentionTermsOf is mentionTermsBuild, remembered per (subscriber, job title). containsExplicitTag
// asks for the terms once per message per subscriber, and each ask built a map and slice and
// lowercased every term (#4598: board.mentionTerms was 2% of the server's allocation at 100 agents).
func mentionTermsOf(subscriberID, jobTitle string) termSet {
	key := [2]string{subscriberID, jobTitle}
	termMemo.Lock()
	ts, ok := termMemo.m[key]
	termMemo.Unlock()
	if ok {
		return ts
	}
	ts.terms = mentionTermsBuild(subscriberID, jobTitle)
	ts.lower = make([]string, len(ts.terms))
	for i, t := range ts.terms {
		ts.lower[i] = strings.ToLower(t)
	}
	termMemo.Lock()
	if termMemo.m == nil || len(termMemo.m) >= termMemoLimit {
		termMemo.m = make(map[[2]string]termSet)
	}
	termMemo.m[key] = ts
	termMemo.Unlock()
	return ts
}

func mentionTermsBuild(subscriberID, jobTitle string) []string {
	terms := []string{"@notify-all", "@notify_all", "@notifyall", "@all"}
	seen := make(map[string]bool)
	for _, t := range terms {
		seen[strings.ToLower(t)] = true
	}

	addTerm := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		lower := strings.ToLower(t)
		if !seen[lower] {
			seen[lower] = true
			terms = append(terms, t)
		}
	}

	for _, name := range []string{subscriberID, jobTitle} {
		if name == "" {
			continue
		}
		addTerm("@" + name)
		clean := strings.ReplaceAll(name, " ", "")
		if clean != "" {
			addTerm("@" + clean)
		}
		addTerm(name + ":")
		addTerm(name + " —")
		addTerm(name + "—")
	}

	return terms
}

// numberedSuffix returns " <digits>" when s continues at i with that and then a word
// boundary (the number of a numbered agent's name, "Backend Dev 2"), else "".
func numberedSuffix(s string, i int) string {
	if i >= len(s) || s[i] != ' ' {
		return ""
	}
	j := i + 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j > i+1 && (j == len(s) || !isAlphaNum(s[j])) {
		return s[i:j]
	}
	return ""
}

// numberedNames is the set of a board's active subscriber ids that end in " <digits>"
// (numbered agents, #3543), lowercased, for containsExplicitTag.
func (s *Store) numberedNames(ctx context.Context, q sqlx.QueryerContext, project string) map[string]bool {
	names, _ := s.numberedNamesErr(ctx, q, project)
	return names
}

// numberedNamesErr is numberedNames that also reports a failed read.
func (s *Store) numberedNamesErr(ctx context.Context, q sqlx.QueryerContext, project string) (map[string]bool, error) {
	var ids []string
	if err := sqlx.SelectContext(ctx, q, &ids, `SELECT subscriber_id FROM board_subscribers WHERE project = ? AND is_active = 1`, project); err != nil {
		return nil, err
	}
	var out map[string]bool
	for _, id := range ids {
		lower := strings.ToLower(id)
		if i := strings.LastIndexByte(lower, ' '); i > 0 && numberedSuffix(lower, i) != "" && i+len(numberedSuffix(lower, i)) == len(lower) {
			if out == nil {
				out = map[string]bool{}
			}
			out[lower] = true
		}
	}
	return out, nil
}

func isAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// containsExplicitTag returns true if content explicitly mentions or tags subscriberID/jobTitle,
// or includes broadcast tags like @all, @notify-all.
//
// numbered (optional) is the board's numbered agent names, lowercased (numberedNames):
// "@Backend Dev 2" skips "Backend Dev" only when "backend dev 2" is one of them, so
// "@Orchestrator 3 tasks are ready" still tags the Orchestrator on a board without an
// "Orchestrator 3" (Security's #3543 review).
func containsExplicitTag(content, subscriberID, jobTitle string, numbered ...map[string]bool) bool {
	var names map[string]bool
	if len(numbered) > 0 {
		names = numbered[0]
	}
	contentLower := strings.ToLower(content)
	ts := mentionTermsOf(subscriberID, jobTitle)
	for ti, term := range ts.terms {
		tLower := ts.lower[ti]
		idx := 0
		for {
			pos := strings.Index(contentLower[idx:], tLower)
			if pos == -1 {
				break
			}
			matchPos := idx + pos
			endPos := matchPos + len(tLower)
			idx = endPos

			// Check word boundary before match: should not be preceded by alphanumeric (e.g. user@domain.com or mydev:)
			if matchPos > 0 && isAlphaNum(contentLower[matchPos-1]) {
				continue
			}

			// Check word boundary at the end if the term starts with '@' (e.g. @Dev in @Developer)
			if strings.HasPrefix(term, "@") {
				if endPos < len(contentLower) && isAlphaNum(contentLower[endPos]) {
					continue
				}
				// "@Backend Dev 2" names the numbered agent "Backend Dev 2", not "Backend Dev"
				// (#3543), when the board has that agent.
				if num := numberedSuffix(contentLower, endPos); num != "" && names[tLower[1:]+num] {
					continue
				}
			}
			return true
		}
	}
	return false
}

// isOrchestrator returns true if the subscriber identity or job title indicates an orchestrator,
// or if canPeek is enabled.
// IsOrchestrator reports whether an identity gets the orchestrator's rights (receive
// every message, plan tasks, peek): can_peek, or "orchestrator" in its id or job title.
func IsOrchestrator(subscriberID, jobTitle string, canPeek int) bool {
	return isOrchestrator(subscriberID, jobTitle, canPeek)
}

func isOrchestrator(subscriberID, jobTitle string, canPeek int) bool {
	if canPeek != 0 {
		return true
	}
	if strings.Contains(strings.ToLower(subscriberID), "orchestrator") {
		return true
	}
	if strings.Contains(strings.ToLower(jobTitle), "orchestrator") {
		return true
	}
	return false
}

// confirmedMention returns a message matching where (and args) that tags subscriberID, or 0:
// the newest one when newest is set, else any (a cheaper plan, for a yes/no). The SQL LIKE only finds candidates; each is confirmed with the rule
// ReadMessages and CheckUnread use (containsExplicitTag with the board's numbered names: word
// boundaries, and "@Backend Dev 2" isn't "@Backend Dev" on a board that has a "Backend Dev 2"),
// so the notifier never nudges an agent for a message its read wouldn't show it (#3549).
func (s *Store) confirmedMention(ctx context.Context, project, subscriberID, jobTitle, where string, args []interface{}, from int64, newest bool) (int64, error) {
	terms := mentionTerms(subscriberID, jobTitle)
	if len(terms) == 0 {
		return 0, nil
	}
	// The numbered names are read before the candidates' cursor is opened, so this never holds
	// two reader connections at once (Security's #3549 review).
	numbered := s.numberedNamesCached(ctx, project)
	order := ""
	if newest {
		order = " ORDER BY m.id DESC"
	}
	// From the mention index (#4483): the messages that can mention the subscriber, found by key, then
	// the LIKE rule and the confirming rule as before. An index that cannot answer, or fails, reads as
	// it did: the OR of LIKE patterns over every message in the window.
	if tokens := s.mentionIndexTokensJSON(ctx, terms); tokens != "" {
		patterns := mentionLikePatterns(subscriberID, jobTitle)
		idxArgs := append(append([]interface{}{}, args...), tokens, from)
		query := "SELECT m.id, m.content FROM board_messages m WHERE " + where + " AND " + mentionIndexClause("m.") + order
		id, err := s.confirmFrom(ctx, s.hot().cachedStmt(ctx, s.reader(), query), query, idxArgs, patterns, subscriberID, jobTitle, numbered)
		if err == nil {
			s.mentionIndexAnswers.Add(1)
			return id, nil
		}
	}
	parts := make([]string, len(terms))
	args = append([]interface{}{}, args...)
	for i, term := range terms {
		parts[i] = "m.content LIKE ? COLLATE NOCASE"
		args = append(args, "%"+term+"%")
	}
	return s.confirmFrom(ctx, nil, "SELECT m.id, m.content FROM board_messages m WHERE "+where+" AND ("+strings.Join(parts, " OR ")+")"+order, args, nil, subscriberID, jobTitle, numbered)
}

// confirmFrom runs a candidate query and returns the first message that tags the subscriber; when patterns is
// set, a candidate must also match them as SQLite's LIKE would (the index's candidates are a superset of the
// LIKE query's).
func (s *Store) confirmFrom(ctx context.Context, st *sqlx.Stmt, query string, args []interface{}, patterns []string, subscriberID, jobTitle string, numbered map[string]bool) (int64, error) {
	rows, err := queryxHot(ctx, st, s.reader(), query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return 0, err
		}
		if patterns != nil && !likeAny(content, patterns) {
			continue
		}
		if containsExplicitTag(content, subscriberID, jobTitle, numbered) {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// likeMentionCandidates is the query ReadMessages used before the mention index (#4465), and still uses for a
// subscriber whose terms the index cannot answer: the messages after a cursor, from others, that match any of
// the LIKE patterns, the first fetchLimit of them, oldest first. The index's results are tested against it.
func (s *Store) likeMentionCandidates(ctx context.Context, project, subscriberID string, lastReadID int64, patterns []string, fetchLimit int) ([]Message, error) {
	whereClauses := make([]string, len(patterns))
	args := []interface{}{project, lastReadID, subscriberID}
	for i, p := range patterns {
		whereClauses[i] = "m.content LIKE ? COLLATE NOCASE"
		args = append(args, p)
	}
	args = append(args, fetchLimit)
	query := fmt.Sprintf(
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.task_id, m.created_at,
		        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
		 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ? AND (%s)
		 ORDER BY m.id ASC LIMIT ?`,
		strings.Join(whereClauses, " OR "))
	var candidates []Message
	err := s.reader().SelectContext(ctx, &candidates, query, args...)
	return candidates, err
}
