package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// newTestServer is New for tests: the server's background work stops and its board database
// closes when the test ends, before its temp folders are removed. Windows can't delete an open
// database (#3914).
func newTestServer(t testing.TB, cfg *config.Config, db *store.DB, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal) *Server {
	t.Helper()
	srv := mustServer(NewWithKey(cfg, db, backend, terminal, ""))
	t.Cleanup(srv.Close)
	return srv
}

// mustServer is NewWithKey's server, for tests whose setup can't fail.
func mustServer(srv *Server, err error) *Server {
	if err != nil {
		panic(err)
	}
	return srv
}

// jsonString is s as a JSON string literal, quotes included. Request bodies built around a path
// use it: a Windows path's backslashes made a hand-quoted body invalid JSON (#3914).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// setHome makes dir the home directory os.UserHomeDir reports: HOME on Unix, USERPROFILE on
// Windows (#3914).
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// tildePath is how the hub shows a folder under the home: "~" and the rest with forward slashes on
// every system (the Hub's "~/folder" form has one separator, #7070: hubDisplayPath turns a Windows path's
// backslashes into slashes for a request through the Hub).
func tildePath(rel ...string) string {
	return "~/" + strings.Join(rel, "/")
}

// homeForms are the spellings of the home directory dir that no answer to the hub may contain:
// as given and resolved, and each as it appears inside JSON, where a Windows path's backslashes
// are escaped, so a check for the raw path alone could never fail there (#3914).
func homeForms(dir string) []string {
	forms := []string{dir}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		forms = append(forms, real)
	}
	for _, f := range forms {
		if q := strings.Trim(jsonString(f), `"`); q != f {
			forms = append(forms, q)
		}
	}
	return forms
}
