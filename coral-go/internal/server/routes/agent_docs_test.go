package routes

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// missingFS is a docs folder that isn't there (a dev build that never synced agent_docs).
type missingFS struct{}

func (missingFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// #3532: a build without agent docs lists none as [] (never null, which the Docs tab reads
// as a failed load), and README comes first.
func TestAgentDocsList(t *testing.T) {
	list := func(fsys fstest.MapFS) string {
		rec := httptest.NewRecorder()
		NewAgentDocsHandler(fsys).List(rec, httptest.NewRequest("GET", "/api/agent-docs", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	if got := list(fstest.MapFS{}); got != "[]" {
		t.Errorf("no docs: got %s, want []", got)
	}
	rec := httptest.NewRecorder()
	NewAgentDocsHandler(missingFS{}).List(rec, httptest.NewRequest("GET", "/api/agent-docs", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("no docs folder: got %d %s, want 200 []", rec.Code, rec.Body.String())
	}
	got := list(fstest.MapFS{"hooks.md": {Data: []byte("# h")}, "README.md": {Data: []byte("# r")}, "x.txt": {Data: []byte("x")}})
	if !strings.HasPrefix(got, `[{"name":"README"`) || !strings.Contains(got, `"name":"hooks"`) || strings.Contains(got, "x.txt") {
		t.Errorf("got %s", got)
	}
}
