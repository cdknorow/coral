package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A server test that builds a real Server must close it before the test ends, so its board database
// (messageboard.db, plus the WAL and SHM) is released before t.TempDir is removed: Windows cannot
// delete an open file, and a pre-#3914 run left 88 internal/server tests failing their TempDir
// cleanup for exactly this (#4862). The closer is Server.Close (it waits for background work, then
// closes boardStore). newTestServer registers it; a test that calls the constructor directly must
// register it too (t.Cleanup(srv.Close), after t.TempDir so it runs first by LIFO).
//
// This guard keeps that discipline: every *_test.go that constructs a Server either goes through
// newTestServer or registers a Server close, unless it is the one test whose construction is meant
// to fail (no Server is returned, so there is nothing to close).
func TestEveryServerTestClosesItsBoardStore(t *testing.T) {
	// The constructor by its own name, not httptest.NewServer or a router's New.
	constructs := regexp.MustCompile(`\bNewWithKey\(|\bmustServer\(New|=\s*NewServer\(cfg`)
	// A registration that ends the server: newTestServer (which does t.Cleanup(srv.Close)), or a
	// direct srv/s.Close as a cleanup value or call.
	closes := regexp.MustCompile(`\bnewTestServer\(|[a-zA-Z0-9_]*srv[0-9]*\.Close\b|\bs\.Close\b|Cleanup\(srv`)
	// The only test whose construction is designed to return an error (srv is nil): #4157's api-key
	// error. It asserts srv == nil, so there is no Server to close; its sessions.db is closed by the
	// test, and the constructor closes the board store itself on that error path.
	allow := map[string]bool{
		"keystore_error_test.go": true, // NewWithKey returns an error; srv is nil
		"test_server_test.go":    true, // defines newTestServer, the closer
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, f := range files {
		if allow[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		if constructs.MatchString(s) && !closes.MatchString(s) {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		t.Errorf("these server tests build a Server but never close it, so its board database is still open when t.TempDir is removed (Windows fails the cleanup, #4862); route them through newTestServer or add t.Cleanup(srv.Close):\n%s", strings.Join(bad, "\n"))
	}
}
