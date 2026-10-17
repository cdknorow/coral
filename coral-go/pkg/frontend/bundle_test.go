package frontend

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

var (
	bundleOnce sync.Once
	bundleVal  *Bundle
	bundleErr  error
)

func realBundle(t *testing.T) (*Bundle, fs.FS) {
	t.Helper()
	sub, err := fs.Sub(Static, "static")
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(Templates, "templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	bundleOnce.Do(func() { bundleVal, bundleErr = BuildBundle(sub, index) })
	if bundleErr != nil {
		t.Fatalf("BuildBundle: %v", bundleErr)
	}
	return bundleVal, sub
}

// The startup graph is inside app.js, and every module in it has a stub at its own URL.
func TestBundleHasEveryStartupModuleOnceAndAStubForEach(t *testing.T) {
	b, static := realBundle(t)
	app := string(b.Files["app.js"])
	if len(b.Modules) < 100 {
		t.Fatalf("only %d modules in the bundle; the startup graph has about 130", len(b.Modules))
	}
	if !bytes.Contains(b.Files["app.js"], []byte("sourceMappingURL=app.js.map")) || len(b.Files["app.js.map"]) == 0 {
		t.Error("app.js has no source map, so an error would name app.js only")
	}
	// Inside the bundle nothing is imported by a relative URL any more: that would be a second copy of the module.
	if m := regexp.MustCompile(`(?m)\bfrom\s*["']\.{1,2}/`).FindString(app); m != "" {
		t.Errorf("app.js still imports a module by URL: %q", m)
	}
	for _, m := range b.Modules {
		if m == "app.js" {
			continue
		}
		stub, ok := b.Files[m]
		if !ok {
			t.Errorf("no stub for startup module %s", m)
			continue
		}
		if len(stub) > 4096 {
			t.Errorf("stub for %s is %d bytes: it should only re-export", m, len(stub))
		}
		want, err := exportNames(static, m)
		if err != nil {
			t.Fatal(err)
		}
		got := stubNames(string(stub))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("stub for %s exports %v, the module exports %v", m, got, want)
		}
	}
}

var stubExport = regexp.MustCompile(`\b[\w$]+\$[\w$]+ as ([\w$]+)`)

func stubNames(stub string) []string {
	var out []string
	for _, m := range stubExport.FindAllStringSubmatch(stub, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// A stub's names are live bindings of the bundle's own instance: the bundle exports each alias, and the stub imports it from app.js
// by the right relative path (a module in a subdirectory climbs out of it).
func TestBundleStubsReExportFromAppJsByRelativePath(t *testing.T) {
	b, _ := realBundle(t)
	for m, stub := range b.Files {
		if m == "app.js" || !strings.HasSuffix(m, ".js") {
			continue
		}
		want := fmt.Sprintf("from '%s'", relativeTo(m, "app.js"))
		if !bytes.Contains(stub, []byte(want)) && string(stub) != "export {};\n" {
			t.Errorf("stub %s: want %s, got %q", m, want, stub)
		}
	}
	if got := relativeTo("cloud/x.js", "app.js"); got != "../app.js" {
		t.Errorf("relativeTo cloud/x.js = %s", got)
	}
	if got := relativeTo("state.js", "app.js"); got != "./app.js" {
		t.Errorf("relativeTo state.js = %s", got)
	}
}

// Every module that is not in the bundle (loaded later by importOnce) must still resolve every name it imports from a startup
// module through that module's stub. esbuild is the check: it refuses an import of a name the file does not export.
func TestEveryLazyModuleBuildsAgainstTheStubs(t *testing.T) {
	b, static := realBundle(t)
	inBundle := map[string]bool{}
	for _, m := range b.Modules {
		inBundle[m] = true
	}
	var lazy []string
	_ = fs.WalkDir(static, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".min.js") ||
			strings.HasPrefix(name, "vendor/") || strings.HasPrefix(name, "agent_docs/") || inBundle[name] {
			return nil
		}
		lazy = append(lazy, name)
		return nil
	})
	if len(lazy) < 20 {
		t.Fatalf("only %d lazy modules found", len(lazy))
	}
	var entry strings.Builder
	for i, m := range lazy {
		fmt.Fprintf(&entry, "import * as l%d from './%s';\nglobalThis.l%d = l%d;\n", i, m, i, i)
	}
	// Resolve through the bundled prefix's files, with the stubs standing where the startup modules were. A few lazy modules are
	// scripts (not modules) or need globals; esbuild reports only import/export problems, which is what is checked.
	res := bundleBuild(&overlayFS{files: b.Files, base: static}, entry.String(), "probe.js", false)
	for _, e := range res.Errors {
		loc := ""
		if e.Location != nil {
			loc = fmt.Sprintf(" (%s:%d)", e.Location.File, e.Location.Line)
		}
		t.Errorf("lazy modules against the stubs: %s%s", e.Text, loc)
	}
}

// The shell's 24 stylesheets become one, in the shell's order, with the same bytes each.
func TestBundleStylesheetIsTheShellsStylesheetsInOrder(t *testing.T) {
	b, static := realBundle(t)
	index, _ := fs.ReadFile(Templates, "templates/index.html")
	links := StylesheetsInShell(index)
	if len(links) < 20 {
		t.Fatalf("found %d stylesheet links in the shell; it has 24", len(links))
	}
	var want bytes.Buffer
	for _, l := range links {
		data, err := fs.ReadFile(static, l)
		if err != nil {
			t.Fatal(err)
		}
		want.Write(MinifyAsset(l, data))
		want.WriteByte('\n')
	}
	if !bytes.Equal(b.Files[BundleStylesheet], want.Bytes()) {
		t.Error("css/bundle.css is not the shell's stylesheets in order")
	}
	// A relative url() in a stylesheet resolves against the bundle's own directory: it must be the original's (css/ next to vendor/).
	if path.Dir(BundleStylesheet) != "css" {
		t.Errorf("the bundle stylesheet moved out of css/: %s", BundleStylesheet)
	}
	for _, l := range links {
		data, _ := fs.ReadFile(static, l)
		for _, m := range regexp.MustCompile(`url\(([^)]+)\)`).FindAllStringSubmatch(string(data), -1) {
			u := strings.Trim(m[1], `"' `)
			if strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "#") || strings.HasPrefix(u, "/") {
				continue
			}
			if path.Dir(l) != "css" {
				t.Errorf("%s has a relative url() but is not in css/", l)
			}
		}
	}
}

// Same input, same bytes (every viewer gets one answer, and the server builds it once).
func TestBundleIsDeterministic(t *testing.T) {
	b, static := realBundle(t)
	index, _ := fs.ReadFile(Templates, "templates/index.html")
	again, err := BuildBundle(static, index)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range b.Files {
		if !bytes.Equal(data, again.Files[name]) {
			t.Errorf("%s differs between two builds", name)
		}
	}
	if len(b.Files) != len(again.Files) {
		t.Errorf("%d files then %d", len(b.Files), len(again.Files))
	}
}

// The bundled prefix serves the bundle's files and, for every other path, the original.
func TestBundleOverlay(t *testing.T) {
	b, static := realBundle(t)
	o := b.FS(static)
	app, err := fs.ReadFile(o, "app.js")
	if err != nil || !bytes.Equal(app, b.Files["app.js"]) {
		t.Fatalf("app.js is not the bundle: %v", err)
	}
	orig, _ := fs.ReadFile(static, "password_ui.js")
	got, err := fs.ReadFile(o, "password_ui.js")
	if err != nil || !bytes.Equal(got, orig) {
		t.Errorf("a file the bundle does not replace is not the original: %v", err)
	}
	st, err := fs.Stat(o, "state.js")
	if err != nil || st.IsDir() || st.Size() != int64(len(b.Files["state.js"])) {
		t.Errorf("state.js stat: %v %v", st, err)
	}
	if _, err := fs.Stat(o, "no-such-file.js"); err == nil {
		t.Error("a missing file is found")
	}
}

func TestBundleDisabledBySwitch(t *testing.T) {
	t.Setenv("CORAL_NO_BUNDLE", "1")
	if !BundleDisabled() {
		t.Error("CORAL_NO_BUNDLE=1 does not disable the bundle")
	}
	t.Setenv("CORAL_NO_BUNDLE", "")
	if BundleDisabled() {
		t.Error("an empty CORAL_NO_BUNDLE disables the bundle")
	}
}

// A source that imports by a bare name or out of the tree is refused, not fetched from the disk.
func TestBundleRefusesImportsOutsideTheTree(t *testing.T) {
	mem := fakeFS{"app.js": "import 'fs';\n"}
	if _, err := BuildBundle(mem, nil); err == nil {
		t.Error("a bare import was bundled")
	}
	mem = fakeFS{"app.js": "import '../etc/passwd.js';\n"}
	if _, err := BuildBundle(mem, nil); err == nil {
		t.Error("an import out of the tree was bundled")
	}
}

type fakeFS map[string]string

func (f fakeFS) Open(name string) (fs.File, error) {
	if s, ok := f[name]; ok {
		return &memFile{name: path.Base(name), r: bytes.NewReader([]byte(s)), size: int64(len(s))}, nil
	}
	return nil, fs.ErrNotExist
}
