package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #6204: only a dev build has a profiler. A production file that imports net/http/pprof (which registers handlers that serve goroutine and heap dumps) fails here: it has to carry the dev build tag.
func TestOnlyDevBuildsImportTheProfilerServer(t *testing.T) {
	goRoot, _ := repoRoots(t)
	var bad []string
	for _, src := range productionSources(t) {
		imports := false
		for _, im := range src.file.Imports {
			if im.Path.Value == `"net/http/pprof"` {
				imports = true
			}
		}
		if !imports {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(goRoot, filepath.FromSlash(src.rel)))
		if err != nil {
			t.Fatal(err)
		}
		first := strings.SplitN(string(raw), "\n", 2)[0]
		if !strings.HasPrefix(first, "//go:build dev") || strings.Contains(first, "!dev") {
			bad = append(bad, src.rel)
		}
	}
	assert.Empty(t, bad, "a file that imports net/http/pprof must start with //go:build dev")
}
