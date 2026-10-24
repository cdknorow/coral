package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// the SessionTurn of c90a22e6e (round 1), kept here as the reference: every event in order.
func (s *TaskStore) oldSessionTurn(ctx context.Context, sessionID string) (RunTurn, error) {
	var rows []struct {
		Type   string         `db:"event_type"`
		Detail sql.NullString `db:"detail_json"`
		At     string         `db:"created_at"`
	}
	if err := s.reader().SelectContext(ctx, &rows,
		`SELECT event_type, detail_json, created_at FROM agent_events WHERE session_id = ? AND event_type IN ('prompt_submit', 'stop', 'session_reset', 'tool_use', 'permission_request') ORDER BY id`, sessionID); err != nil {
		return RunTurn{}, err
	}
	turn := RunTurn{}
	for _, e := range rows {
		switch e.Type {
		case "prompt_submit":
			turn = RunTurn{State: TurnWorking}
		case "session_reset":
			turn = RunTurn{}
		case "tool_use", "permission_request":
			if turn.State != TurnNone {
				turn = RunTurn{State: TurnWorking}
			}
		case "stop":
			if turn.State == TurnNone {
				continue
			}
			turn = RunTurn{State: TurnDone, At: e.At}
			var d struct {
				Failed bool `json:"failed"`
			}
			if e.Detail.Valid && json.Unmarshal([]byte(e.Detail.String), &d) == nil && d.Failed {
				turn.State = TurnFailed
			}
		}
	}
	return turn, nil
}

// QA #7110 round 2: the two bounded reads give the same answer as the full scan on every sequence of events.
func TestSessionTurnBoundedReadsAgreeWithTheFullScan(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ctx := t.Context()
	rng := rand.New(rand.NewSource(7110))
	types := []string{"prompt_submit", "prompt_submit", "stop", "stop", "session_reset", "tool_use", "tool_use", "permission_request", "notification", "session_end", "auto_accept", "status"}
	details := []string{"", `{"failed":false}`, `{"failed":true}`, `not json`}
	disagreements := 0
	for i := 0; i < 400; i++ {
		sid := "rand-" + string(rune('a'+i%26)) + "-" + json.Number(string(rune('0'+i/26%10))).String() + "-" + string(rune('A'+i/260))
		n := rng.Intn(14)
		for j := 0; j < n; j++ {
			typ := types[rng.Intn(len(types))]
			ev := &AgentEvent{AgentName: "a", EventType: typ, SessionID: &sid}
			if typ == "stop" {
				if d := details[rng.Intn(len(details))]; d != "" {
					ev.DetailJSON = &d
				}
			}
			_, err := ts.InsertAgentEvent(ctx, ev)
			require.NoError(t, err)
		}
		want, err := ts.oldSessionTurn(ctx, sid)
		require.NoError(t, err)
		got, err := ts.SessionTurn(ctx, sid)
		require.NoError(t, err)
		if want != got {
			disagreements++
			t.Errorf("session %s (%d events): full scan %+v, bounded %+v", sid, n, want, got)
		}
	}
	require.Zero(t, disagreements)
}
