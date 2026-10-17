package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4308: a tick that reuses the previous tick's serialized rows must send exactly what a full
// rebuild sends. Each tick here changes random inputs of random agents (display name, icon, name
// color, sleeping, model and board in the live row, state and goal events, the log, which agents
// exist), then compares the incremental build, byte for byte, with a build that has no previous tick.

func TestWSLocalPartReuseEqualsAFullRebuild(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	logDir := t.TempDir()
	term := newMockTerminal()
	h := NewSessionsHandler(db, &config.Config{LogDir: logDir}, nil, term, nil)
	ctx := context.Background()
	req := httptest.NewRequest("GET", "/ws/coral", nil)
	rng := rand.New(rand.NewSource(4308))

	const n = 12
	type agent struct {
		sid, tmux, dir string
		present        bool
	}
	agents := make([]*agent, n)
	live := func(a *agent, mod func(*store.LiveSession)) {
		ls := &store.LiveSession{SessionID: a.sid, AgentType: "claude", AgentName: "a-" + a.sid[:4], WorkingDir: a.dir}
		if mod != nil {
			mod(ls)
		}
		require.NoError(t, h.ss.RegisterLiveSession(ctx, ls))
	}
	for i := range agents {
		sid := fmt.Sprintf("%08x-2222-3333-4444-555555555555", i+1)
		agents[i] = &agent{sid: sid, tmux: naming.SessionName("claude", sid), dir: fmt.Sprintf("/tmp/agent%d", i), present: true}
		term.addSession(agents[i].tmux, agents[i].dir)
		live(agents[i], nil)
	}

	strs := []string{"", "Ada", "Grace", "Linus", "x y z", `quote " and \ slash`, "ünïcode ✓"}
	ops := []func(a *agent){
		func(a *agent) { require.NoError(t, h.ss.SetDisplayName(ctx, a.sid, strs[rng.Intn(len(strs))])) },
		func(a *agent) {
			var ic *string
			if s := strs[rng.Intn(len(strs))]; s != "" {
				ic = &s
			}
			require.NoError(t, h.ss.SetIcon(ctx, a.sid, ic))
		},
		func(a *agent) {
			c := []string{"#aabbcc", "#112233"}[rng.Intn(2)]
			require.NoError(t, h.ss.SetNameColor(ctx, a.sid, &c))
		},
		func(a *agent) { live(a, func(ls *store.LiveSession) { ls.IsSleeping = rng.Intn(2) }) },
		func(a *agent) {
			live(a, func(ls *store.LiveSession) {
				m := []string{"claude-opus-4-1", "claude-sonnet-4", "unknown-model"}[rng.Intn(3)]
				b := strs[rng.Intn(len(strs))]
				ls.Model, ls.BoardName = &m, &b
			})
		},
		func(a *agent) {
			live(a, func(ls *store.LiveSession) {
				c := []string{`{"a":1}`, `{ "b" : [1, 2] }`, `not json`, ""}[rng.Intn(4)]
				ls.Capabilities = &c
			})
		},
		func(a *agent) {
			et := []string{"prompt_submit", "tool_use", "stop", "notification", "goal", "permission_request"}[rng.Intn(6)]
			sum := strs[rng.Intn(len(strs))]
			sid := a.sid
			_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "a-" + a.sid[:4], SessionID: &sid, EventType: et, Summary: sum})
			require.NoError(t, err)
		},
		func(a *agent) { // a log, long idle so its staleness is stable across the two builds
			p := naming.LogFile(logDir, "claude", a.sid)
			require.NoError(t, os.WriteFile(p, []byte(fmt.Sprintf("line %d\n", rng.Intn(5))), 0o644))
			old := time.Now().Add(-2*time.Hour - time.Duration(rng.Intn(3000))*time.Second)
			require.NoError(t, os.Chtimes(p, old, old))
		},
		func(a *agent) {
			if a.present {
				term.KillSession(ctx, a.tmux, "", "")
			} else {
				term.addSession(a.tmux, a.dir)
			}
			a.present = !a.present
		},
	}

	asMap := func(p *wsLocalPart) map[string]string {
		m := map[string]string{}
		for i, k := range p.keys {
			if k != "" {
				m[k] = p.serialized[i]
			}
		}
		return m
	}
	var prev *wsLocalPart
	totalReused := 0
	for tick := 0; tick < 60; tick++ {
		for c := rng.Intn(3); c > 0; c-- { // 0 to 2 changes; 0 is a tick with nothing new
			ops[rng.Intn(len(ops))](agents[rng.Intn(n)])
		}
		got := h.buildWSLocalPart(req, tick%7 == 3, prev)
		want := h.buildWSLocalPart(req, tick%7 == 3, nil)
		require.NoError(t, got.err)
		assert.Equal(t, asMap(want), asMap(got), "tick %d: the reused build sends what a full rebuild sends", tick)
		assert.Equal(t, want.runsJSON, got.runsJSON)
		for i, k := range got.keys { // and every serialized row is exactly its entry
			if k == "" {
				continue
			}
			b, err := json.Marshal(got.sessions[i])
			require.NoError(t, err)
			assert.Equal(t, string(b), got.serialized[i], "tick %d row %s", tick, k)
		}
		totalReused += got.reused
		prev = got
	}
	assert.Greater(t, totalReused, 100, "most rows of most ticks are reused")

	// Nothing changed: every row comes from the previous tick, and none is serialized again.
	again := h.buildWSLocalPart(req, prev.hasRemotes, prev)
	assert.Equal(t, asMap(prev), asMap(again))
	assert.Equal(t, len(asMap(again)), again.reused, "an unchanged tick reuses every row")
}

// sameJSONValue is "the same to json.Marshal": bit-for-bit floats, no coercion between types,
// and anything it does not know is a difference.
func TestSameJSONValue(t *testing.T) {
	sp := func(s string) *string { return &s }
	neg0 := math.Copysign(0, -1)
	for _, tc := range []struct {
		a, b any
		same bool
	}{
		{nil, nil, true}, {nil, "", false}, {"a", "a", true}, {"a", "b", false}, {"1", 1, false},
		{true, true, true}, {true, false, false}, {1, 1, true}, {1, int64(1), false}, {int64(2), int64(2), true},
		{1.5, 1.5, true}, {0.0, neg0, false}, {math.NaN(), math.NaN(), true}, {1.0, 1, false},
		{json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1}`), true}, {json.RawMessage(`{"a":1}`), json.RawMessage(`{"a": 1}`), false},
		{map[string]string{"x": "y"}, map[string]string{"x": "y"}, true}, {map[string]string{"x": "y"}, map[string]string{"x": "z"}, false},
		{map[string]string(nil), map[string]string{}, false},
		{map[string]any{"a": 1, "b": []any{"s", nil}}, map[string]any{"a": 1, "b": []any{"s", nil}}, true},
		{map[string]any{"a": 1}, map[string]any{"a": 2}, false}, {map[string]any{"a": 1}, map[string]any{"b": 1}, false},
		{[]any{1, 2}, []any{1}, false},
		{struct{ X int }{1}, struct{ X int }{1}, false}, // a type it does not know is a difference
		{(*string)(nil), (*string)(nil), true}, {sp("a"), sp("a"), true}, {sp("a"), sp("b"), false}, {sp("a"), (*string)(nil), false},
		{(*string)(nil), nil, false}, {sp("a"), "a", false},
		{(*int)(nil), (*int)(nil), false}, // a pointer it does not know is a difference
	} {
		assert.Equal(t, tc.same, sameJSONValue(tc.a, tc.b), "%#v vs %#v", tc.a, tc.b)
	}
}
