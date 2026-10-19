package startup

import (
	"bytes"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The startup line says what it is and carries no leftover diagnostic marker (#5044).
func TestStartupLineHasNoDiagnosticMarker(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	logStartup(Options{})
	line := buf.String()
	require.Contains(t, line, "[STARTUP]")
	assert.NotContains(t, line, "DIAGNOSTIC")
	assert.NotContains(t, line, "marker=")
	assert.Contains(t, line, "version=")
}

// No source file of the server names a DEV-TMUX-DIAGNOSTIC marker: they were put in to tell builds apart
// while a tmux fault was hunted (#3506) and stayed in every production log line.
func TestNoDiagnosticMarkerLeftInTheSource(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == ".git") {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("DEV-TMUX-DIAGNOSTIC")) {
			found = append(found, path)
		}
		return nil
	}))
	assert.Empty(t, found)
}
