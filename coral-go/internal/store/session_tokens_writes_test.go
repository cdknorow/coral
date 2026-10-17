package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #4303 (Security's INFO 2): the token-owner cache is invalidated by the writes in
// session_tokens.go (mint) and sessions.go (a session unregistered or replaced). A write to
// session_tokens from anywhere else would change a token without invalidating the cache, so a
// revoked token could be accepted from it for up to its TTL. This fails if one appears: add the
// invalidation (db.InvalidateSessionTokens() after the commit) and the file to the list below.
func TestSessionTokensAreWrittenOnlyWhereTheCacheIsInvalidated(t *testing.T) {
	write := regexp.MustCompile("(?is)(\\bUPDATE\\s+(OR\\s+\\w+\\s+)?|\\bINSERT\\s+(OR\\s+\\w+\\s+)?INTO\\s+|\\bDELETE\\s+FROM\\s+|\\bREPLACE\\s+INTO\\s+)\"?`?session_tokens\\b")
	allowed := map[string]bool{
		filepath.Join("internal", "store", "session_tokens.go"): true,
		filepath.Join("internal", "store", "sessions.go"):       true,
	}
	root := filepath.Join("..", "..") // the module root: coral-go
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if write.Match(b) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("a write to session_tokens outside the files that invalidate the token cache: %v", found)
	}

	// The pattern itself: it sees the spellings a write can take.
	for _, sql := range []string{
		"UPDATE session_tokens SET revoked_at = ?",
		"update  session_tokens set x = 1",
		"INSERT INTO session_tokens (token_hash) VALUES (?)",
		"INSERT OR REPLACE INTO session_tokens (token_hash) VALUES (?)",
		"DELETE FROM session_tokens WHERE 1",
		"REPLACE INTO session_tokens VALUES (1)",
		"UPDATE\n\tsession_tokens SET a = 1",
	} {
		if !write.MatchString(sql) {
			t.Errorf("the pattern misses %q", sql)
		}
	}
	for _, sql := range []string{
		"SELECT session_id FROM session_tokens WHERE token_hash = ?",
		"CREATE TABLE IF NOT EXISTS session_tokens (",
		"CREATE INDEX idx_session_tokens_session ON session_tokens(session_id)",
		"UPDATE session_tokens_other SET a = 1",
	} {
		if write.MatchString(sql) {
			t.Errorf("the pattern flags %q", sql)
		}
	}
}
