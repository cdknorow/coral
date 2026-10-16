package server

import (
	"encoding/json"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// newTestServer is New for tests: the server's background work stops and its board database
// closes when the test ends, before its temp folders are removed. Windows can't delete an open
// database (#3914).
func newTestServer(t testing.TB, cfg *config.Config, db *store.DB, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal) *Server {
	t.Helper()
	srv := New(cfg, db, backend, terminal)
	t.Cleanup(srv.Close)
	return srv
}

// jsonString is s as a JSON string literal, quotes included. Request bodies built around a path
// use it: a Windows path's backslashes made a hand-quoted body invalid JSON (#3914).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
