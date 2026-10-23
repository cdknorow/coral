package frontend

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded static tree goes into every Coral server binary (and the Hub's), so what it holds
// costs every download. A 6.86 MB logo nothing referenced (static/corral.png) shipped in every
// binary until #3854. These checks keep that from coming back.

const (
	// staticBudget is the most the embedded static tree may hold. It is about 4.5 MB (4.1 MB plus the
	// agent docs that `make` syncs into static/agent_docs); raise it deliberately, with the reason in
	// the commit, when a real asset needs the room.
	staticBudget = 6<<20 + 148<<10 // 6 MiB + 148 KiB (159: +12 KiB more for the first-run launch changes (the agent choice, Use this folder, the pending image bubble: 7 KB), measured the way CI does with the agent docs synced; +8 KiB before that for the Jobs list on a phone, the accessible names and contrast tokens, the keyboard fixes and the notices region, about 7 KB in all; #5987: the embedded tree passed 6 MiB with the hub's two Connect pictures and the small (i) module; the text cut removes more than it adds as it lands)
	// fileBudget is the most one embedded file may hold (the CodeMirror bundle, the largest, is ~1 MB).
	fileBudget = 1536 << 10
)

// unreferencedImages are images nothing in this repository names, kept because something outside it
// may load them by URL (/static/<name>): Coral Hub serves this tree, and the icons are small.
var unreferencedImages = map[string]string{
	"coral_logo.png":       "logo, may be linked by URL from outside the repo (1 KB)",
	"coral_logo.svg":       "logo, may be linked by URL from outside the repo (9 KB)",
	"coral_tray.png":       "tray icon, may be linked by URL from outside the repo (1 KB)",
	"coral-icon-88x88.png": "icon, may be linked by URL from outside the repo (4 KB)",
}

func TestStaticTreeWithinBudget(t *testing.T) {
	var total int64
	err := fs.WalkDir(Static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if info.Size() > fileBudget {
			t.Errorf("%s is %d bytes, over the %d-byte budget for one embedded file: every Coral binary carries it", p, info.Size(), fileBudget)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total > staticBudget {
		t.Errorf("the embedded static tree is %d bytes, over its %d-byte budget (staticBudget): every Coral binary carries it", total, staticBudget)
	}
	t.Logf("embedded static tree: %d bytes (budget %d)", total, staticBudget)
}

func isImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svg", ".avif", ".bmp":
		return true
	}
	return false
}

// TestStaticImagesAreReferenced fails on an embedded image whose file name appears nowhere in the
// static text files, the templates or the module's Go sources.
func TestStaticImagesAreReferenced(t *testing.T) {
	var images []string
	var corpus strings.Builder
	for _, root := range []struct {
		fsys fs.FS
		dir  string
	}{{Static, "static"}, {Templates, "templates"}} {
		err := fs.WalkDir(root.fsys, root.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if isImage(p) {
				if root.dir == "static" {
					images = append(images, p)
				}
				return nil
			}
			b, err := fs.ReadFile(root.fsys, p)
			if err != nil {
				return err
			}
			corpus.Write(b)
			corpus.WriteByte('\n')
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The module's Go sources (the server serves some icons by name, e.g. internal/server/static_assets.go).
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(moduleRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" || n == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "static_budget_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		corpus.Write(b)
		corpus.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The Docs pages (coral-go/agent_docs) name their screenshots in static/docs-img (#5038). The copy `make` syncs into static/agent_docs is not in a clean checkout, so the
	// sources are read here.
	docs, err := filepath.Glob(filepath.Join(moduleRoot, "agent_docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		b, err := os.ReadFile(d)
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(b)
		corpus.WriteByte('\n')
	}
	if len(images) == 0 {
		t.Fatal("found no images in the static tree; the walk is wrong")
	}
	text := corpus.String()
	for _, p := range images {
		name := path.Base(p)
		if strings.Contains(text, name) {
			if why, ok := unreferencedImages[name]; ok {
				t.Errorf("%s is referenced now; drop it from unreferencedImages (%s)", p, why)
			}
			continue
		}
		if _, ok := unreferencedImages[name]; ok {
			continue
		}
		t.Errorf("%s is embedded in every Coral binary but nothing names it (static text files, templates, Go sources): delete it, or reference it", p)
	}
	for name := range unreferencedImages {
		if _, err := fs.Stat(Static, "static/"+name); err != nil {
			t.Errorf("unreferencedImages lists %s, which is not in the static tree: drop it", name)
		}
	}
}
