package routes

import (
	"encoding/json"
	"reflect"
)

// An agent's token sees the live roster: who is running, on which board, in what state. It does not see what other boards' agents are working
// on (#5041, found in the v1.4.105 release QA): the row of a session that is not the agent's own or on its board carries only the fields below,
// never the prompt, the first prompt, the summary, the waiting text, the branch, the working directory, the log path, the git state or any text a
// later change adds to a row (an allow-list: a new field is private to the Operator until it is listed here). The agent's own row and its board
// mates' rows are unchanged, and so is everything the Operator, the dashboard and the command line (no agent token) get.
var rosterFields = map[string]bool{
	"name": true, "agent_type": true, "session_id": true, "tmux_session": true, "display_name": true, "icon": true, "name_color": true,
	"board_project": true, "board_label": true, "board_job_title": true, "board_is_orchestrator": true,
	"status": true, "sleeping": true, "archived": true, "working": true, "waiting_for_input": true, "waiting_kind": true, "awaiting_user": true,
	"not_started": true, "done": true, "stuck": true, "staleness_seconds": true, "model": true, "thinking": true, "list_at": true, "server": true,
}

// agentMayReadRow says whether the row is the agent's own or on its board (board is the agent's board, "" when it has none). A row from another
// server (a registered remote's) is never one of them.
func agentMayReadRow(row map[string]any, ownerSessionID, board string) bool {
	if sid, _ := row["session_id"].(string); sid != "" && sid == ownerSessionID {
		return true
	}
	if srv, _ := row["server"].(string); srv != "" && srv != "local" {
		return false
	}
	bp, _ := row["board_project"].(string)
	return board != "" && bp == board
}

// agentBoardOf is the board of the agent's own row among rows ("" when it has none, or none is listed).
func agentBoardOf(rows []map[string]any, ownerSessionID string) string {
	for _, row := range rows {
		if sid, _ := row["session_id"].(string); sid != "" && sid == ownerSessionID {
			if srv, _ := row["server"].(string); srv != "" && srv != "local" {
				continue
			}
			bp, _ := row["board_project"].(string)
			return bp
		}
	}
	return ""
}

// rosterRow is a copy of row with only the roster fields (the rows are shared with other connections: never changed in place).
func rosterRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(rosterFields))
	for k, v := range row {
		if rosterFields[k] {
			out[k] = v
		}
	}
	return out
}

// scopeRowsForAgent is rows as the agent whose session this is may see them: other boards' rows reduced to the roster fields, the rest as they are.
// The slice and the rows it keeps are the caller's; the reduced rows are copies.
func scopeRowsForAgent(rows []map[string]any, ownerSessionID string) []map[string]any {
	board := agentBoardOf(rows, ownerSessionID)
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		if agentMayReadRow(row, ownerSessionID, board) {
			out[i] = row
		} else {
			out[i] = rosterRow(row)
		}
	}
	return out
}

// scopeTickForAgent is the shared /ws/coral tick as the agent's socket may see it: the same rows reduced as scopeRowsForAgent does, with the
// serialized forms the connection diffs on recomputed for the reduced rows (the others keep theirs). The shared tick is never changed.
func scopeTickForAgent(tick *wsTick, ownerSessionID string) *wsTick {
	board := agentBoardOf(tick.sessions, ownerSessionID)
	t := *tick
	t.sessions = make([]map[string]any, 0, len(tick.sessions))
	copies := make(map[uintptr]map[string]any)
	dropped := make(map[uintptr]bool) // another server's rows: an agent's socket carries none of them
	for _, row := range tick.sessions {
		if srv, _ := row["server"].(string); srv != "" && srv != "local" {
			dropped[reflect.ValueOf(row).Pointer()] = true
			continue
		}
		if agentMayReadRow(row, ownerSessionID, board) {
			t.sessions = append(t.sessions, row)
			continue
		}
		cp := rosterRow(row)
		copies[reflect.ValueOf(row).Pointer()] = cp
		t.sessions = append(t.sessions, cp)
	}
	t.servers, t.hasRemotes, t.serversJSON = nil, false, "" // nor the list of the remote servers
	t.current = make(map[string]string, len(tick.current))
	t.byKey = make(map[string]map[string]any, len(tick.byKey))
	for key, s := range tick.byKey {
		if dropped[reflect.ValueOf(s).Pointer()] {
			continue
		}
		if cp, ok := copies[reflect.ValueOf(s).Pointer()]; ok {
			b, _ := json.Marshal(cp)
			t.current[key], t.byKey[key] = string(b), cp
			continue
		}
		t.current[key], t.byKey[key] = tick.current[key], s
	}
	return &t
}
