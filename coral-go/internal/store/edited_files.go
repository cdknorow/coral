package store

import (
	"context"
	"fmt"
	"strings"
)

// The live session list shows each session's count of distinct files edited (Write and Edit tool
// events). Counting them from agent_events on every build was 85% of the build's time with 40
// agents and 250,000 events (#3846, #3833 item 3: about 70 ms a build on epoch). The counts are
// kept in agent_event_files by triggers on agent_events, so every writer keeps them right (a
// prune, a clear, an UPDATE, a raw INSERT), and a count is a lookup: a session's row per file,
// with how many of its events name the file.

// editedFileRow is the condition, for row (NEW or OLD), that an event names an edited file; the
// same rows COUNT(DISTINCT json_extract(detail_json, '$.file_path')) counted.
func editedFileRow(row string) string {
	return strings.NewReplacer("R.", row+".").Replace(`R.event_type = 'tool_use' AND R.tool_name IN ('Write', 'Edit')
		AND R.session_id IS NOT NULL AND R.detail_json IS NOT NULL AND json_valid(R.detail_json)
		AND json_extract(R.detail_json, '$.file_path') IS NOT NULL`)
}

func editedFileAdd(row string) string {
	return fmt.Sprintf(`INSERT INTO agent_event_files (session_id, file_path, n)
		VALUES (%[1]s.session_id, json_extract(%[1]s.detail_json, '$.file_path'), 1)
		ON CONFLICT (session_id, file_path) DO UPDATE SET n = n + 1;`, row)
}

func editedFileDrop(row string) string {
	return fmt.Sprintf(`UPDATE agent_event_files SET n = n - 1
		WHERE session_id = %[1]s.session_id AND file_path = json_extract(%[1]s.detail_json, '$.file_path');
		DELETE FROM agent_event_files
		WHERE session_id = %[1]s.session_id AND file_path = json_extract(%[1]s.detail_json, '$.file_path') AND n <= 0;`, row)
}

// ensureEditedFiles creates agent_event_files and its triggers, filling it from agent_events the
// first time, in one transaction, so no event is counted twice or missed.
func (d *DB) ensureEditedFiles(ctx context.Context) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_event_files'`); err != nil {
		return err
	}
	fresh := n == 0
	cols := `session_id, event_type, tool_name, detail_json`
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_event_files (
			session_id TEXT NOT NULL,
			file_path  TEXT NOT NULL,
			n          INTEGER NOT NULL,
			PRIMARY KEY (session_id, file_path)
		) WITHOUT ROWID`,
		`CREATE TRIGGER IF NOT EXISTS agent_event_files_insert AFTER INSERT ON agent_events
			WHEN ` + editedFileRow("NEW") + ` BEGIN ` + editedFileAdd("NEW") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS agent_event_files_delete AFTER DELETE ON agent_events
			WHEN ` + editedFileRow("OLD") + ` BEGIN ` + editedFileDrop("OLD") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS agent_event_files_update_old AFTER UPDATE OF ` + cols + ` ON agent_events
			WHEN ` + editedFileRow("OLD") + ` BEGIN ` + editedFileDrop("OLD") + ` END`,
		`CREATE TRIGGER IF NOT EXISTS agent_event_files_update_new AFTER UPDATE OF ` + cols + ` ON agent_events
			WHEN ` + editedFileRow("NEW") + ` BEGIN ` + editedFileAdd("NEW") + ` END`,
	}
	if fresh {
		stmts = append(stmts, `INSERT INTO agent_event_files (session_id, file_path, n)
			SELECT session_id, json_extract(detail_json, '$.file_path'), COUNT(*) FROM agent_events
			WHERE `+strings.ReplaceAll(editedFileRow("agent_events"), "agent_events.", "")+`
			GROUP BY 1, 2 HAVING true
			ON CONFLICT (session_id, file_path) DO UPDATE SET n = n + excluded.n`)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("edited-file counts: %w", err)
		}
	}
	return tx.Commit()
}
