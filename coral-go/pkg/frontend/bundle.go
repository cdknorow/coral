package frontend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

// The bundled family (#4228). The dashboard starts from app.js and statically imports about 130 modules, each its own request; on a
// slow link the request count costs more than the bytes (163 files over six connections: about 4.2 s at 150 ms RTT with no bandwidth
// cap). BuildBundle builds app.js's static import graph into ONE file, and the shell's 24 stylesheets into one, which the server
// serves under its own versioned prefix (/static/bb/<build>/) next to the unchanged files under /static/b/<build>/.
//
// Modules loaded later by importOnce() are fetched by URL and import the startup modules by their relative URLs ('./state.js'). Left
// alone they would get a second copy of every module they import, with its own state. So in the bundled prefix each startup module's
// URL is a stub that re-exports, as live bindings, the instance inside the bundle:
//
//	export { m12$a as a, m12$b as b } from './app.js';
//
// and the bundle itself exports every startup module's names under those aliases. A lazy module, a test importing a module by URL, and
// the bundle's own code all see one instance. The agent pop-out is the same app.js but boots a different mode and stays on the
// original files: a stub at an original URL would load the dashboard's boot code there.
//
// The result is a pure function of the embedded files: every viewer and every request gets the same bytes, so it is built once.
// CORAL_NO_BUNDLE=1 turns the family off (the dashboard then uses the original files, as before #4228).

// BundleStylesheet is the bundled stylesheet's path relative to the bundled prefix (next to the originals so ../vendor/ urls hold).
const BundleStylesheet = "css/bundle.css"

// Bundle is the set of files the bundled prefix serves instead of the original at the same path; every other path is the original.
type Bundle struct {
	Files map[string][]byte
	// Modules are the startup modules (paths relative to static/) that are inside app.js.
	Modules []string
}

// BundleDisabled reports whether CORAL_NO_BUNDLE=1 asks for the original files.
func BundleDisabled() bool { return os.Getenv("CORAL_NO_BUNDLE") == "1" }

var stylesheetLink = regexp.MustCompile(`<link rel="stylesheet" href="\{\{ \$base \}\}(css/[^"]+\.css)">`)

// StylesheetsInShell returns the stylesheets the shell template links in its head, in order.
func StylesheetsInShell(indexHTML []byte) []string {
	var out []string
	for _, m := range stylesheetLink.FindAllSubmatch(indexHTML, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// bundleFormat names what the bundled family's bytes depend on beyond the sources and MinifyAsset's options: change it when BuildBundle's output changes
// for the same tree (its esbuild options, what it registers or adds), so the static build version (MinifyIdentity, #4320) changes with it and a browser
// holding the old bundle at the same /static/bb/<build>/ URL is not served the new one's stubs by mistake.
const bundleFormat = "bundle:ws+syntax+idents,self-registered-importonce,startup-lazy:1"

// StartupLazyModules are the modules the dashboard's startup imports with importOnce (not statically), on every normal start: the
// leftover-tmux check and the sign-in and set-password prompts. They are part of the bundle so those imports cost
// no request, and no second round trip for the stubs they import (#4326). Order matters: password_ui.js awaits importOnce('./password_rules.js') at its
// top level, which is answered from the bundle only once password_rules.js has run, so that comes first. (hub_notice.js used to await password_ui.js the same way; it asks for it when it needs it now, so it can be bundled.) bundle_startup_requests_browser checks the list against a real start.
var StartupLazyModules = []string{"terminal_backend.js", "password_rules.js", "password_ui.js", "hub_notice.js", "local_auth_banner.js", "import_once.js"}

var importOnceSpec = regexp.MustCompile(`importOnce\(\s*['"]\./([^'"?]+)['"]`)

// importOnceSpecs is the set of modules (paths under static/) that some file imports with importOnce('./path').
func importOnceSpecs(static fs.FS) map[string]bool {
	out := map[string]bool{}
	_ = fs.WalkDir(static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		if data, err := fs.ReadFile(static, p); err == nil {
			for _, m := range importOnceSpec.FindAllSubmatch(data, -1) {
				out[string(m[1])] = true
			}
		}
		return nil
	})
	return out
}

// startupEntry is the bundle's entry before its aliases: app.js, then the modules startup imports lazily.
func startupEntry() string {
	var b strings.Builder
	b.WriteString("import './app.js';\n")
	for _, m := range StartupLazyModules {
		fmt.Fprintf(&b, "import './%s';\n", m)
	}
	return b.String()
}

// BuildBundle builds the family from the static tree (rooted at static/) and the shell template's source.
func BuildBundle(static fs.FS, indexHTML []byte) (*Bundle, error) {
	// 1. The bundle: app.js, then an alias export for every name of every module it contains.
	probe := bundleBuild(static, startupEntry(), "app.js", false, nil)
	if err := buildError(probe); err != nil {
		return nil, err
	}
	modules := moduleNames(probe)
	var names []string
	for _, m := range modules {
		if m != "app.js" {
			names = append(names, m)
		}
	}
	sort.Strings(names)
	exports := map[string][]string{}
	for _, m := range names {
		ex, err := exportNames(static, m)
		if err != nil {
			return nil, err
		}
		exports[m] = ex
	}
	var entry strings.Builder
	entry.WriteString(startupEntry())
	alias := map[string]string{}
	for i, m := range names {
		alias[m] = fmt.Sprintf("m%d", i)
		if len(exports[m]) == 0 {
			continue
		}
		entry.WriteString("export { ")
		for j, e := range exports[m] {
			if j > 0 {
				entry.WriteString(", ")
			}
			fmt.Fprintf(&entry, "%s as %s$%s", e, alias[m], e)
		}
		fmt.Fprintf(&entry, " } from './%s';\n", m)
	}
	// A module some importOnce('./path') names registers its own namespace with importOnce when it has run (below), so a later importOnce
	// is answered from the bundle with no request for the stub (#4326). Registered by the module itself, not by the entry: the entry runs after
	// app.js, whose startup already calls importOnce. Only those modules get one: a namespace object for every module costs about 20 KB of gzip.
	imported := importOnceSpecs(static)
	registered := map[string]bool{}
	for _, m := range names {
		if imported[m] && m != "import_once.js" {
			registered[m] = true
		}
	}
	res := bundleBuild(static, entry.String(), "app.js", true, registered)
	if err := buildError(res); err != nil {
		return nil, err
	}
	b := &Bundle{Files: map[string][]byte{}, Modules: modules}
	for _, f := range res.OutputFiles {
		switch {
		case strings.HasSuffix(f.Path, ".js.map"):
			b.Files["app.js.map"] = f.Contents
		case strings.HasSuffix(f.Path, ".js"):
			b.Files["app.js"] = f.Contents
		}
	}
	if len(b.Files["app.js"]) == 0 {
		return nil, fmt.Errorf("bundle: esbuild produced no app.js")
	}

	// 2. A stub at each startup module's own URL.
	for _, m := range names {
		var s strings.Builder
		if len(exports[m]) > 0 {
			s.WriteString("export { ")
			for j, e := range exports[m] {
				if j > 0 {
					s.WriteString(", ")
				}
				fmt.Fprintf(&s, "%s$%s as %s", alias[m], e, e)
			}
			s.WriteString(" } from '")
			s.WriteString(relativeTo(m, "app.js"))
			s.WriteString("';\n")
		} else {
			// A module with no exports runs for its effects: those ran inside the bundle.
			s.WriteString("export {};\n")
		}
		b.Files[m] = []byte(s.String())
	}

	// 3. The stylesheets, in the shell's order, as one file.
	if links := StylesheetsInShell(indexHTML); len(links) > 0 {
		var css bytes.Buffer
		for _, l := range links {
			data, err := fs.ReadFile(static, l)
			if err != nil {
				return nil, fmt.Errorf("bundle: %s: %w", l, err)
			}
			css.Write(MinifyAsset(l, data))
			css.WriteByte('\n')
		}
		b.Files[BundleStylesheet] = css.Bytes()
	}
	return b, nil
}

// relativeTo is the import specifier for target, as written in the module at from (both paths under static/).
func relativeTo(from, target string) string {
	dir := path.Dir(from)
	if dir == "." {
		return "./" + target
	}
	up := strings.Repeat("../", strings.Count(dir, "/")+1)
	return up + target
}

func buildError(r api.BuildResult) error {
	if len(r.Errors) > 0 {
		e := r.Errors[0]
		loc := ""
		if e.Location != nil {
			loc = fmt.Sprintf(" (%s:%d)", e.Location.File, e.Location.Line)
		}
		return fmt.Errorf("bundle: %s%s", e.Text, loc)
	}
	return nil
}

// bundleBuild bundles entry (source text, resolved as app.js's sibling) with the static tree as the file system.
func bundleBuild(static fs.FS, entry, sourcefile string, final bool, registered map[string]bool) api.BuildResult {
	opts := api.BuildOptions{
		Stdin:         &api.StdinOptions{Contents: entry, Sourcefile: sourcefile, Loader: api.LoaderJS, ResolveDir: "/"},
		Bundle:        true,
		Format:        api.FormatESModule,
		Target:        api.ESNext,
		Charset:       api.CharsetUTF8,
		LegalComments: api.LegalCommentsInline,
		Outfile:       "/out/app.js", // names the map "app.js.map" in the file's sourceMappingURL comment
		Write:         false,
		Metafile:      true,
		LogLevel:      api.LogLevelSilent,
		Plugins:       []api.Plugin{embeddedFiles(static, registered)},
	}
	if final {
		// Whitespace, syntax and identifiers. MinifyAsset keeps the source's names so a stack trace names what the source names; the bundle
		// has a source map (below), which devtools and error reporters read, so its names are mangled: -23% raw and -14% gzip on app.js
		// (1,071 KB to 826 KB; 284 KB to 245 KB gzip), which is both download and parse time on a phone (#4326). Nothing reads a function or class name (no
		// fn.name, constructor.name or toString), and every export keeps its name, so the stubs, the tests and window assignments hold.
		opts.MinifyWhitespace, opts.MinifySyntax, opts.MinifyIdentifiers = true, true, true
		opts.Sourcemap = api.SourceMapLinked
		opts.SourcesContent = api.SourcesContentInclude
	}
	return api.Build(opts)
}

// embeddedFiles resolves './x.js' and '../x.js' against the importing file inside fsys: esbuild reads no disk.
func embeddedFiles(fsys fs.FS, registered map[string]bool) api.Plugin {
	const ns = "static"
	return api.Plugin{Name: "embedded-static", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if !strings.HasPrefix(a.Path, "./") && !strings.HasPrefix(a.Path, "../") {
				return api.OnResolveResult{}, fmt.Errorf("bundle: only relative imports are bundled, got %q (from %s)", a.Path, a.Importer)
			}
			dir := ""
			if a.Namespace == ns {
				dir = path.Dir(a.Importer)
			}
			name := path.Clean(path.Join(dir, a.Path))
			if strings.HasPrefix(name, "../") || name == ".." {
				return api.OnResolveResult{}, fmt.Errorf("bundle: %q leaves the static tree (from %s)", a.Path, a.Importer)
			}
			return api.OnResolveResult{Path: name, Namespace: ns}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: ns}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			f, err := fsys.Open(a.Path)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			defer f.Close()
			data, err := io.ReadAll(f)
			if err != nil {
				return api.OnLoadResult{}, err
			}
			src := string(data)
			if registered[a.Path] {
				// The module's own namespace, registered with importOnce once the module has run (a module may import itself).
				src += fmt.Sprintf("\nimport * as __bundledSelf from './%s';\nimport { registerBundled as __registerBundled } from '%s';\n__registerBundled({ %q: __bundledSelf });\n", path.Base(a.Path), relativeTo(a.Path, "import_once.js"), a.Path)
			}
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
		})
	}}
}

type metafile struct {
	Inputs  map[string]struct{ Bytes int }
	Outputs map[string]struct{ Exports []string }
}

// moduleNames lists the source modules inside the bundle (paths under static/), from the build's metafile.
func moduleNames(r api.BuildResult) []string {
	var mf metafile
	_ = json.Unmarshal([]byte(r.Metafile), &mf)
	var out []string
	for in := range mf.Inputs {
		if name, ok := strings.CutPrefix(in, "static:"); ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// exportNames lists the names module m exports (re-exports included), read by esbuild with no bundling.
func exportNames(static fs.FS, m string) ([]string, error) {
	data, err := fs.ReadFile(static, m)
	if err != nil {
		return nil, err
	}
	r := api.Build(api.BuildOptions{
		Stdin:    &api.StdinOptions{Contents: string(data), Sourcefile: m, Loader: api.LoaderJS},
		Bundle:   false,
		Format:   api.FormatESModule,
		Target:   api.ESNext,
		Outdir:   "/out",
		Write:    false,
		Metafile: true,
		LogLevel: api.LogLevelSilent,
	})
	if err := buildError(r); err != nil {
		return nil, fmt.Errorf("%s: %w", m, err)
	}
	var mf metafile
	if err := json.Unmarshal([]byte(r.Metafile), &mf); err != nil {
		return nil, err
	}
	var exports []string
	for _, o := range mf.Outputs {
		exports = append(exports, o.Exports...)
	}
	sort.Strings(exports)
	return exports, nil
}

// FS returns the bundled prefix's file system: the bundle's files over base (the original static tree).
func (b *Bundle) FS(base fs.FS) fs.FS { return &overlayFS{files: b.Files, base: base} }

type overlayFS struct {
	files map[string][]byte
	base  fs.FS
}

// Built reports whether name is one of the bundle's own (final) files rather than an original.
func (o *overlayFS) Built(name string) bool { _, ok := o.files[name]; return ok }

func (o *overlayFS) Open(name string) (fs.File, error) {
	if data, ok := o.files[name]; ok {
		return &memFile{name: path.Base(name), r: bytes.NewReader(data), size: int64(len(data))}, nil
	}
	return o.base.Open(name)
}

type memFile struct {
	name string
	r    *bytes.Reader
	size int64
}

func (f *memFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *memFile) Close() error               { return nil }
func (f *memFile) Stat() (fs.FileInfo, error) { return memInfo{f}, nil }

type memInfo struct{ f *memFile }

func (i memInfo) Name() string       { return i.f.name }
func (i memInfo) Size() int64        { return i.f.size }
func (i memInfo) Mode() fs.FileMode  { return 0o444 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }
