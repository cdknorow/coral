package routes

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #6018: a launch that cannot find tmux says what was missing and where it looked, with the PATH's entry count and never the PATH itself.
func TestALaunchThatCannotFindTmuxSaysWhatWasMissingWithoutThePath(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	marker := filepath.Join(t.TempDir(), "profile-folder-marker")
	t.Setenv("PATH", marker+string(filepath.ListSeparator)+filepath.Join(marker, "bin")+string(filepath.ListSeparator)+filepath.Join("/", "usr", "bin"))
	logTmuxNotFound()
	out := buf.String()
	assert.Contains(t, out, "launch: tmux not found")
	assert.Contains(t, out, "binary=tmux", "what was missing")
	assert.Contains(t, out, "the PATH logged at server start", "where it looked")
	assert.Contains(t, out, "path_entries=3", "and how long that PATH is")
	assert.NotContains(t, out, "profile-folder-marker", "never the PATH")
}
