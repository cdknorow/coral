package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// #6219: a project reached through a symlink (macOS's /tmp is /private/tmp) is filed by Claude Code under its resolved path; a session started under the other name and resumed has a file in each folder.
// Coral looked at the folder as named first and kept reading the older file, so a switch's confirmation, written to the live one, was never seen.
func TestTheTranscriptOfAProjectReachedThroughASymlinkIsTheOneWrittenLast(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	realDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "project-link")
	require.NoError(t, os.Symlink(realDir, link))
	const sid = "00000000-0000-4000-8000-000000006219"
	file := func(dir string) string {
		p := filepath.Join(base, transcriptlink.ClaudeProjectDirName(dir), sid+".jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		return p
	}
	write := func(p string, at time.Time) {
		require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
		require.NoError(t, os.Chtimes(p, at, at))
	}
	named, resolved := file(link), file(realDir)
	require.NotEqual(t, named, resolved)
	now := time.Now()

	write(named, now.Add(-time.Minute))
	require.Equal(t, named, resolveClaudeTranscript(sid, link), "only the folder as named exists")

	write(resolved, now)
	require.Equal(t, resolved, resolveClaudeTranscript(sid, link), "after a resume Claude writes to the resolved folder: that file is the live one, not the older one under the name Coral was given")

	write(named, now.Add(time.Minute))
	require.Equal(t, named, resolveClaudeTranscript(sid, link), "whichever was written last")

	require.NoError(t, os.Remove(named))
	require.Equal(t, resolved, resolveClaudeTranscript(sid, link), "only the resolved folder exists")
	require.Equal(t, resolved, resolveClaudeTranscript(sid, realDir))
	require.Equal(t, resolved, ResolveClaudeTranscript(sid, ""), "with no folder given every project folder is searched")
}
