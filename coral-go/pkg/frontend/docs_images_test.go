package frontend

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Docs page shows its own screenshots (#5038): ![alt](docs-img/<name>.png), a file in static/docs-img that utils.js turns into this server's own URL for it. A page naming a
// file that is not embedded would show a broken image, and an embedded screenshot no page names is weight in every binary.
var docImageRef = regexp.MustCompile(`!\[[^\]]*\]\(docs-img/([^)\s]*)\)`)

var docImageName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.png$`)

func TestDocsImagesAreEmbeddedAndNamed(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join("..", "..", "agent_docs", "*.md"))
	if err != nil || len(docs) == 0 {
		t.Fatalf("no docs found (%v)", err)
	}
	named := map[string]bool{}
	for _, d := range docs {
		b, err := os.ReadFile(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range docImageRef.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			if !docImageName.MatchString(name) {
				t.Errorf("%s names docs-img/%s: a docs image is a lower-case name ending in .png (utils.js shows no other)", filepath.Base(d), name)
				continue
			}
			named[name] = true
			if _, err := fs.Stat(Static, "static/docs-img/"+name); err != nil {
				t.Errorf("%s names docs-img/%s, which is not in static/docs-img", filepath.Base(d), name)
			}
		}
	}
	entries, err := fs.ReadDir(Static, "static/docs-img")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !named[e.Name()] {
			t.Errorf("static/docs-img/%s is named by no page in agent_docs: delete it or use it", path.Base(e.Name()))
		}
	}
}
