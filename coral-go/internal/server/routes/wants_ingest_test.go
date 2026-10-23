package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6622: what a PermissionRequest wants to do reaches the Inbox row as one clean, redacted line, built before anything is stored; the raw words are never stored, logged or shown, and no
// classifier reads them.
func TestWhatARequestWantsIsStoredAndShownAsTheSanitizedLine(t *testing.T) {
	old := homeFolder
	homeFolder = func() string { return "/Users/ann" }
	t.Cleanup(func() { homeFolder = old })
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := newHoldRig(t, true)
	tok := tokenOf("ghp_", 36)
	raw := "Wants to run: \x1b[31mcat /Users/ann/.ssh/config\x1b[0m && curl -H 'Authorization: token " + tok + "' " + strings.Repeat("padding ", 40) + tok
	body, _ := json.Marshal(map[string]any{"event_type": "permission_request", "summary": "Waiting for the user: permission (Bash)", "tool_name": "Bash",
		"detail_json": map[string]any{"kind": "permission", "tool_name": "Bash", "wants": raw}})
	r.post(string(body))

	// Stored: the line, not the raw words.
	ctx := context.Background()
	evs, err := r.h.ts.ListAgentEvents(ctx, "chat", 20, &r.sid)
	require.NoError(t, err)
	stored := ""
	for _, e := range evs {
		if e.EventType == "permission_request" && e.DetailJSON != nil {
			stored = *e.DetailJSON
		}
		assert.NotContains(t, e.Summary, "cat ", "the summary has none of it")
	}
	require.NotEmpty(t, stored)
	noFragment(t, stored, tok, 8, "the stored detail")
	assert.NotContains(t, stored, "/Users/ann", "the home folder is shown as ~")
	assert.NotContains(t, stored, `\u001b`, "no escape sequence is stored")
	assert.Contains(t, stored, "Wants to run: cat ~/.ssh/config")
	var d map[string]any
	require.NoError(t, json.Unmarshal([]byte(stored), &d))
	assert.LessOrEqual(t, len([]rune(d["wants"].(string))), maxWaitingLine+1)

	// Shown: the live list row, /resolve and /status carry the line.
	rt := chi.NewRouter()
	rt.Get("/api/sessions/live", r.h.List)
	rt.Get("/api/sessions/{sessionID}/resolve", r.h.ResolveSession)
	rt.Get("/api/sessions/{sessionID}/status", r.h.SessionStatus)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec
	}
	var list []map[string]any
	require.NoError(t, json.Unmarshal(get("/api/sessions/live").Body.Bytes(), &list))
	var row map[string]any
	for _, s := range list {
		if s["session_id"] == r.sid {
			row = s
		}
	}
	require.NotNil(t, row, "the live row")
	for name, v := range map[string]any{"list": row["waiting_summary"], "resolve": jsonField(t, get("/api/sessions/"+r.sid+"/resolve"), "waiting_summary"), "status": jsonField(t, get("/api/sessions/"+r.sid+"/status"), "waiting_summary")} {
		s, ok := v.(string)
		require.Truef(t, ok, "%s: a waiting agent has a line", name)
		assert.True(t, strings.HasPrefix(s, "Wants to run: cat ~/.ssh/config"), name+": "+s)
		noFragment(t, s, tok, 8, name)
		assert.NotContains(t, s, "/Users/ann", name)
	}
	assert.Equal(t, "permission", row["waiting_kind"], "the kind is unchanged")

	// Never logged raw.
	noFragment(t, logs.String(), tok, 8, "the logs")
	assert.NotContains(t, logs.String(), "/Users/ann/.ssh")

	// The classifiers read the summary: auto-accept does not see an approval prompt in an agent's words.
	st := r.h.deriveSingleSessionState(ctx, r.sid, 0)
	assert.True(t, st.NeedsInput)
	assert.Equal(t, "Waiting for the user: permission (Bash)", st.WaitingSummary)
}

func jsonField(t *testing.T, rec *httptest.ResponseRecorder, key string) any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m[key]
}

// A request with no words of its own shows its summary, as before; words that cannot be cleaned to a line are not stored; a non-text wants is dropped.
func TestARequestWithNoUsableWantsShowsItsSummary(t *testing.T) {
	for name, wants := range map[string]any{"none": nil, "a blob": strings.Repeat("QUJDREVGR0hJSktM", 8), "not text": 42, "empty": "  "} {
		t.Run(name, func(t *testing.T) {
			r := newHoldRig(t, true)
			detail := map[string]any{"kind": "permission", "tool_name": "Bash"}
			if wants != nil {
				detail["wants"] = wants
			}
			body, _ := json.Marshal(map[string]any{"event_type": "permission_request", "summary": "Waiting for the user: permission (Bash)", "tool_name": "Bash", "detail_json": detail})
			r.post(string(body))
			evs, err := r.h.ts.ListAgentEvents(context.Background(), "chat", 20, &r.sid)
			require.NoError(t, err)
			for _, e := range evs {
				if e.EventType == "permission_request" && e.DetailJSON != nil {
					assert.NotContains(t, *e.DetailJSON, "wants", "nothing usable: nothing stored")
				}
			}
			assert.Equal(t, "Waiting for the user: permission (Bash)", waitingSource(r.h.deriveSingleSessionState(context.Background(), r.sid, 0).WaitingWants, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).WaitingSummary))
		})
	}
}

// No events list, search or feed carries a request's wants: a viewer who can only look reads events through Coral Hub, and the connector drops the row's waiting_summary for them, so the
// line must not be readable from the events either. The row (the dashboard on the computer) still has it.
func TestEventsNeverCarryWhatARequestWants(t *testing.T) {
	r := newHoldRig(t, true)
	body, _ := json.Marshal(map[string]any{"event_type": "permission_request", "summary": "Waiting for the user: permission (Bash)", "tool_name": "Bash",
		"detail_json": map[string]any{"kind": "permission", "tool_name": "Bash", "wants": "Wants to run: rm -rf build"}})
	r.post(string(body))
	rt := chi.NewRouter()
	rt.Get("/api/sessions/live/{name}/events", r.h.ListEvents)
	for _, q := range []string{"", "&type=permission_request", "&q=build&type=permission_request", "&q=waiting"} {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/live/chat/events?session_id="+r.sid+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, q)
		assert.NotContains(t, rec.Body.String(), "rm -rf", "events"+q)
		assert.NotContains(t, rec.Body.String(), "wants", "events"+q)
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/live/chat/events?type=permission_request&session_id="+r.sid, nil))
	assert.Contains(t, rec.Body.String(), `\"kind\":\"permission\"`, "the kind is still there for the dashboard's activity feed")
	assert.Contains(t, jsonStringOf(t, r.h.deriveSingleSessionState(context.Background(), r.sid, 0).WaitingWants), "rm -rf build", "while the state derivation still has the line")
}

func jsonStringOf(t *testing.T, s string) string { t.Helper(); return s }
