package routes

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3281: a changed row as a field-level diff: identity, changed fields, and an explicit null
// for each field it no longer has (never omitted: omitted keeps the old value).
func TestFieldDelta(t *testing.T) {
	prev := map[string]any{"session_id": "s1", "name": "a", "agent_type": "claude", "working": false, "summary": "x", "branch": "main", "staleness_seconds": 3}
	curr := map[string]any{"session_id": "s1", "name": "a", "agent_type": "claude", "working": true, "branch": "main", "staleness_seconds": 4, "new_field": []any{1}}
	d := fieldDelta(prev, curr)
	assert.Equal(t, map[string]any{
		"_partial": true, "session_id": "s1", "name": "a", "agent_type": "claude",
		"working": true, "staleness_seconds": 4, "new_field": []any{1},
		"summary": nil, // cleared: sent as null
	}, d)
	_, hasBranch := d["branch"]
	assert.False(t, hasBranch, "an unchanged field is left out")

	// A nested value that is equal by JSON is unchanged.
	p2 := map[string]any{"session_id": "s1", "commands": map[string]any{"clear": "/clear"}}
	c2 := map[string]any{"session_id": "s1", "commands": map[string]any{"clear": "/clear"}, "working": true}
	_, has := fieldDelta(p2, c2)["commands"]
	assert.False(t, has)
}

// The feed: with ?diff=fields a dashboard gets partial rows for rows it has; without it,
// whole rows; a hub's feed always gets whole rows.
func TestWSCoral_FieldDiffsAreOptIn(t *testing.T) {
	server, handler := setupTestServer(t)
	ctx := context.Background()
	ss := store.NewSessionStore(handler.db)
	sid := "fielddiff-session-001"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "fd-agent", WorkingDir: "/tmp/fd",
		IsSleeping: 1, CreatedAt: "2026-01-01T00:00:00Z",
	}))

	wsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dial := func(q string) *websocket.Conn {
		c, _, err := websocket.Dial(wsCtx, "ws"+server.URL[4:]+"/ws/coral"+q, nil)
		require.NoError(t, err)
		var first struct {
			Type string `json:"type"`
		}
		require.NoError(t, wsjson.Read(wsCtx, c, &first))
		require.Equal(t, "coral_update", first.Type)
		return c
	}
	fields := dial("?diff=fields")
	defer fields.CloseNow()
	whole := dial("")
	defer whole.CloseNow()
	hub := dial("?feed=hub&diff=fields")
	defer hub.CloseNow()

	rowFor := func(c *websocket.Conn) map[string]any {
		t.Helper()
		for {
			var msg struct {
				Type    string           `json:"type"`
				Changed []map[string]any `json:"changed"`
			}
			require.NoError(t, wsjson.Read(wsCtx, c, &msg))
			for _, e := range msg.Changed {
				if e["session_id"] == sid {
					return e
				}
			}
		}
	}
	require.NoError(t, ss.SetSessionArchived(ctx, sid, true))

	p := rowFor(fields)
	assert.Equal(t, true, p["_partial"], "a partial row")
	assert.Equal(t, true, p["archived"], "with the changed field")
	assert.Equal(t, "fd-agent", p["name"], "and its identity")
	_, hasDir := p["working_directory"]
	assert.False(t, hasDir, "an unchanged field is left out")

	w := rowFor(whole)
	_, partial := w["_partial"]
	assert.False(t, partial, "the default is whole rows")
	assert.Equal(t, "/tmp/fd", w["working_directory"])

	h := rowFor(hub)
	_, partial = h["_partial"]
	assert.False(t, partial, "a hub's feed always gets whole rows")
	assert.Equal(t, "/tmp/fd", h["working_directory"])
}
