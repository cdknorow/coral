package server

import (
	"fmt"
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
	// A site that builds a Server directly: srv := mustServer(NewWithKey(...)), or NewServer(cfg...). The variable is the one to close.
	construct := regexp.MustCompile(`(\w+)\s*,?\s*(?:err)?\s*:?=\s*(?:mustServer\()?(?:NewWithKey|NewServer)\(`)
	// The only test whose construction is designed to return an error (srv is nil): #4157's api-key error. It asserts srv == nil, so
	// there is no Server to close; its sessions.db is closed by the test, and the constructor closes the board store itself on that
	// error path.
	allow := map[string]bool{
		"keystore_error_test.go":            true, // NewWithKey returns an error; srv is nil
		"test_server_test.go":               true, // defines newTestServer, the closer
		"board_store_closed_census_test.go": true, // this file: its comments and patterns name the constructors
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	sites := 0
	for _, f := range files {
		if allow[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			m := construct.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			sites++
			// Each construction site registers its own server's close within the next few lines (t.Cleanup(srv.Close) after
			// t.TempDir, so it runs first by LIFO). A file-wide search let a second server in one test go unclosed behind the first's.
			closer := regexp.MustCompile(`Cleanup\(` + regexp.QuoteMeta(m[1]) + `\.Close\b`)
			end := min(i+5, len(lines))
			if !closer.MatchString(strings.Join(lines[i+1:end], "\n")) {
				bad = append(bad, fmt.Sprintf("%s:%d (%s)", f, i+1, m[1]))
			}
		}
	}
	if sites < 6 {
		t.Fatalf("the census found %d construction sites, want the 7 or so that exist: its pattern no longer sees how a Server is built", sites)
	}
	if len(bad) > 0 {
		t.Errorf("these server tests build a Server and do not register its Close right after, so its board database is still open when t.TempDir is removed (Windows fails the cleanup, #4862); route them through newTestServer or add t.Cleanup(srv.Close) (after t.TempDir, so it runs first): %s", strings.Join(bad, ", "))
	}
}
