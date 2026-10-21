package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #4602 M8: past its panel count or its HTML total a publish is a 409 whose JSON says why (the coral-agent CLI prints
// the whole answer), and nothing is stored; an update of an existing panel still works.
func TestAgentUIPublishRefusalsAreClear(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	_, h, _, ss := setupSessionsTestServer(t)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: "ui-bounds", AgentName: "ui-bounds", AgentType: "codex", WorkingDir: "/tmp"}))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	h.RegisterAgentUI(r)
	server := httptest.NewServer(r)
	defer server.Close()
	put := func(id, html string) (int, map[string]any) {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"title": "T", "html": html})
		req, err := http.NewRequest("PUT", server.URL+"/api/agent/ui/"+id+"?session_id=ui-bounds", bytes.NewReader(data))
		require.NoError(t, err)
		resp, err := agentClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	for i := 0; i < store.MaxAgentUIPanels; i++ {
		code, _ := put(fmt.Sprintf("p%d", i), "<p/>")
		require.Equal(t, 200, code, i)
	}
	code, out := put("extra", "<p/>")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "panel_limit", out["code"])
	assert.Contains(t, out["message"], "at most 20 panels")
	assert.Equal(t, out["error"], out["message"])
	code, _ = put("p5", "<b/>")
	assert.Equal(t, 200, code, "an update of a panel that exists works at the limit")

	// The HTML total: 15 panels of 2 MiB and one just under it leave 16 bytes of room in the 32 MiB (the other four panels hold 4 bytes each).
	big := strings.Repeat("a", 2<<20)
	for i := 0; i < 15; i++ {
		code, out = put(fmt.Sprintf("p%d", i), big)
		require.Equal(t, 200, code, "%d: %v", i, out)
	}
	code, out = put("p15", strings.Repeat("a", 2<<20-32))
	require.Equal(t, 200, code, "%v", out)
	code, out = put("p16", strings.Repeat("x", 64))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "panel_html_limit", out["code"])
	assert.Contains(t, out["message"], "32 MiB")
}
