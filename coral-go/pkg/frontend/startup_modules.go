package frontend

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The modules the dashboard loads at startup are the static import graph from app.js: app.js's
// imports, theirs, and so on. A browser only learns of a module when its importer has arrived and
// been parsed, so a cold load fetched them in four sequential waves (#3900). The shell lists them
// as <link rel="modulepreload"> so every request goes out at once. The list is computed from the
// embedded files, is the same for every viewer and every request, and never comes from input.

// staticImport matches a static import or re-export with a relative specifier, and a bare
// `import './x.js'`. Dynamic imports (importOnce) are not matched: those modules load lazily.
var staticImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;'"` + "`" + `]*?\bfrom\s*['"](\./[^'"]+)['"]|^\s*import\s*['"](\./[^'"]+)['"]`)

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`(?m)(^|[^:'"` + "`" + `])//[^\n]*`)
)

var (
	startupOnce    sync.Once
	startupModules []string
)

// StartupModules returns the module paths (relative to /static/, sorted) that the shell's app.js
// loads statically, app.js included. The result is computed once from the embedded files.
func StartupModules() []string {
	startupOnce.Do(func() {
		sub, err := fs.Sub(Static, "static")
		if err != nil {
			return
		}
		startupModules = moduleGraph(sub, "app.js")
	})
	return startupModules
}

// moduleGraph walks the static imports from entry and returns every module reached, sorted.
// A specifier naming a file that does not exist is skipped (a path inside a comment or string).
func moduleGraph(fsys fs.FS, entry string) []string {
	seen := map[string]bool{}
	queue := []string{entry}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[name] {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		seen[name] = true
		src := lineComment.ReplaceAllString(blockComment.ReplaceAllString(string(data), ""), "$1")
		for _, m := range staticImport.FindAllStringSubmatch(src, -1) {
			spec := m[1]
			if spec == "" {
				spec = m[2]
			}
			dep := path.Clean(path.Join(path.Dir(name), spec))
			if strings.HasPrefix(dep, "..") || !strings.HasSuffix(dep, ".js") {
				continue
			}
			if !seen[dep] {
				queue = append(queue, dep)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
