package routes

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
)

// benchLocalPartHandler is a handler with n running agents, for measuring one /ws/coral tick's
// local build (#4308).
func benchLocalPartHandler(tb testing.TB, n int) *SessionsHandler {
	tb.Helper()
	db, err := storetest.Open(tb, filepath.Join(tb.TempDir(), "test.db"))
	require.NoError(tb, err)
	tb.Cleanup(func() { db.Close() })
	term := newMockTerminal()
	for i := 0; i < n; i++ {
		term.addSession(fmt.Sprintf("claude-%08x-2222-3333-4444-555555555555", i), fmt.Sprintf("/tmp/agent%d", i))
	}
	return NewSessionsHandler(db, &config.Config{LogDir: tb.TempDir()}, nil, term, nil)
}

// BenchmarkWSLocalPart is one tick's local build for 20 agents: allocations per tick are the
// number to watch.
func BenchmarkWSLocalPart(b *testing.B) {
	h := benchLocalPartHandler(b, 20)
	req := httptest.NewRequest("GET", "/ws/coral", nil)
	if p := h.buildWSLocalPart(req, false, nil); len(p.sessions) != 20 {
		b.Fatalf("%d sessions, want 20", len(p.sessions))
	}
	b.ReportAllocs()
	b.ResetTimer()
	var prev *wsLocalPart
	for i := 0; i < b.N; i++ {
		prev = h.buildWSLocalPart(req, false, prev)
	}
}
