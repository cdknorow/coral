package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Coral doesn't configure MCP servers (#3644, the operator's decision). Their definitions held
// tokens in env and headers, stored as plain text: in live_sessions.mcp_servers, and inside the
// JSON of saved teams, team members and workflows. WipeMCPConfig removes every copy, and removes
// it for real: the updates run with secure_delete on (the freed space is zeroed, not left with
// the old text in it) and the WAL is checkpointed and truncated afterwards (its frames still
// held the old pages). The live_sessions column stays, unused.

// mcpJSONColumns are the stored JSON documents that could carry an mcpServers object.
var mcpJSONColumns = []struct{ table, column string }{
	{"teams", "config_json"},
	{"team_members", "agent_config_json"},
	{"workflows", "steps_json"},
	{"workflows", "default_agent_json"},
}

// WipeMCPConfig clears every stored MCP server configuration and returns how many rows changed.
// It is safe to run at every start: with nothing left to clear it changes nothing and doesn't
// checkpoint.
func WipeMCPConfig(ctx context.Context, db *DB) (int, error) {
	conn, err := db.Connx(ctx) // secure_delete is a setting of the connection the updates use
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA secure_delete = ON"); err != nil {
		return 0, fmt.Errorf("secure_delete: %w", err)
	}
	changed := 0
	res, err := conn.ExecContext(ctx, "UPDATE live_sessions SET mcp_servers = NULL WHERE mcp_servers IS NOT NULL")
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err == nil {
		changed += int(n)
	}
	for _, c := range mcpJSONColumns {
		var rows []struct {
			ID  int64  `db:"id"`
			Doc string `db:"doc"`
		}
		q := fmt.Sprintf(`SELECT id, %s AS doc FROM %s WHERE %s LIKE '%%mcpServers%%' OR %s LIKE '%%mcp_servers%%'`, c.column, c.table, c.column, c.column)
		if err := conn.SelectContext(ctx, &rows, q); err != nil {
			return changed, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
		}
		for _, r := range rows {
			stripped, ok := stripMCPKeys(r.Doc)
			if !ok {
				continue
			}
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", c.table, c.column), stripped, r.ID); err != nil {
				return changed, err
			}
			changed++
		}
	}
	if changed > 0 {
		// The checkpoint is busy while a reader holds an older snapshot: retry briefly, and say
		// so if it never completes (the old pages would still be in the WAL).
		for attempt := 0; ; attempt++ {
			var busy, logFrames, done int
			if err := conn.QueryRowxContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done); err != nil {
				return changed, fmt.Errorf("wal checkpoint: %w", err)
			}
			if busy == 0 {
				break
			}
			if attempt == 20 {
				return changed, fmt.Errorf("wal checkpoint: still busy after %d tries; the cleared values stay in the WAL until the next checkpoint", attempt+1)
			}
			select {
			case <-ctx.Done():
				return changed, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return changed, nil
}

// stripMCPKeys removes every "mcpServers" and "mcp_servers" key, at any depth, from a JSON
// document. ok is false when the document isn't JSON or has none.
func stripMCPKeys(doc string) (string, bool) {
	if strings.TrimSpace(doc) == "" {
		return doc, false
	}
	var v any
	if json.Unmarshal([]byte(doc), &v) != nil {
		return doc, false
	}
	removed := false
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for _, k := range []string{"mcpServers", "mcp_servers"} {
				if _, ok := t[k]; ok {
					delete(t, k)
					removed = true
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(v)
	if !removed {
		return doc, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return doc, false
	}
	return string(b), true
}
