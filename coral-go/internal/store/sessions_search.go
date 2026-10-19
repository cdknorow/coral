package store

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"
)

// UpsertFTS updates the FTS5 index for a session.
func (s *SessionStore) UpsertFTS(ctx context.Context, sessionID, body string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM session_fts WHERE session_id = ?", sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO session_fts (session_id, body) VALUES (?, ?)",
			sessionID, body)
		return err // FTS5 may not be available
	})
}

// FTSItem is a session's search text for UpsertFTSBatch.
type FTSItem struct {
	SessionID string
	Body      string
}

// ftsBatchIDs is how many sessions' old rows one DELETE removes: session_fts is an FTS5 table, so a delete by session id is a scan of
// every transcript's text, and one statement with many ids scans once for all of them (#4669; UpsertFTS made one scan per session).
const ftsBatchIDs = 100

// UpsertFTSBatch is UpsertFTS for many sessions in few transactions: each chunk of ftsBatchIDs sessions is one transaction, one DELETE of
// their old rows (one scan of the table) and one INSERT each. A session named twice in items keeps its last body. The result is what
// calling UpsertFTS for each, in order, gives.
func (s *SessionStore) UpsertFTSBatch(ctx context.Context, items []FTSItem) error {
	last := make(map[string]int, len(items)) // the last body of each session
	for i, it := range items {
		last[it.SessionID] = i
	}
	var kept []FTSItem
	for i, it := range items {
		if last[it.SessionID] == i {
			kept = append(kept, it)
		}
	}
	for start := 0; start < len(kept); start += ftsBatchIDs {
		chunk := kept[start:min(start+ftsBatchIDs, len(kept))]
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			ids := make([]any, len(chunk))
			for i, it := range chunk {
				ids[i] = it.SessionID
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM session_fts WHERE session_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...); err != nil {
				return err
			}
			for _, it := range chunk {
				if _, err := tx.ExecContext(ctx, "INSERT INTO session_fts (session_id, body) VALUES (?, ?)", it.SessionID, it.Body); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// sanitizeFTSQuery translates a user query into a safe FTS5 expression.
func sanitizeFTSQuery(raw, mode string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	switch mode {
	case "phrase", "and", "or":
	default:
		mode = "phrase"
	}

	if mode == "phrase" {
		cleaned := strings.ReplaceAll(raw, `"`, " ")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			return ""
		}
		return `"` + cleaned + `"`
	}

	// Tokenize: keep "quoted phrases" together, split bare words
	var tokens []string
	i := 0
	for i < len(raw) {
		if raw[i] == '"' {
			j := strings.IndexByte(raw[i+1:], '"')
			var end int
			if j == -1 {
				end = len(raw) - 1
			} else {
				end = i + 1 + j
			}
			tokens = append(tokens, raw[i:end+1])
			i = end + 1
		} else if raw[i] == ' ' || raw[i] == '\t' {
			i++
		} else {
			j := i
			for j < len(raw) && raw[j] != ' ' && raw[j] != '\t' && raw[j] != '"' {
				j++
			}
			word := raw[i:j]
			upper := strings.ToUpper(word)
			if upper != "AND" && upper != "OR" && upper != "NOT" {
				tokens = append(tokens, word)
			}
			i = j
		}
	}

	if len(tokens) == 0 {
		return ""
	}

	joiner := " AND "
	if mode == "or" {
		joiner = " OR "
	}
	return strings.Join(tokens, joiner)
}
