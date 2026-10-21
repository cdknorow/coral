package startup

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverStarts are the ways a program starts an HTTP server without an http.Server literal, which
// can't set ConnContext.
var serverStarts = regexp.MustCompile(`http\.(ListenAndServe|ListenAndServeTLS|Serve|ServeTLS)\(|new\(http\.Server\)`)

// #95 review F2: every HTTP server a program starts attributes its loopback connections
// (ConnContext: auth.ConnContext), so the loopback pass stays per user on each of them. Every
// http.Server literal in non-test code must set ConnContext, and nothing may start a server another
// way. Today there are two: the main server and the profiler.
func TestEveryHTTPServerAttributesItsConnections(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	literals := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		rel, _ := filepath.Rel(root, path)
		for _, line := range strings.Split(src, "\n") {
			if code := strings.TrimSpace(line); !strings.HasPrefix(code, "//") && serverStarts.MatchString(line) {
				t.Errorf("%s starts an HTTP server without an http.Server literal, so without ConnContext: %s", rel, code)
			}
		}
		for i := strings.Index(src, "http.Server{"); i >= 0; {
			literals++
			body := literalBody(src[i+len("http.Server"):])
			assert.Contains(t, body, "ConnContext", "%s: an http.Server without ConnContext (auth.ConnContext): %s", rel, firstLine(src[i:]))
			next := strings.Index(src[i+1:], "http.Server{")
			if next < 0 {
				break
			}
			i += 1 + next
		}
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, literals, 2, "the census found the main server and the profiler")
}

// literalBody is the composite literal that starts at s ("{...}"), through its matching brace.
func literalBody(s string) string {
	depth := 0
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
