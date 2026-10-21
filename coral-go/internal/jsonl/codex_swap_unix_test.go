//go:build !windows

package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// #3599 (Security's SHOULD on #3597): Codex() checks the rollout path, but the Codex agent can
// write the sessions folder, so the file (or its folder) can be swapped for a symlink to
// another file after the check. Every Codex read opens through a root on the sessions folder,
// so the swap is refused at open time and the outside file's text never reaches the reader.
func TestCodexRolloutSwappedForALinkAfterTheCheckIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	thread := "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	day := filepath.Join(home, "sessions", "2026", "10", "15")
	require.NoError(t, os.MkdirAll(day, 0o700))
	rollout := filepath.Join(day, "rollout-2026-10-15T10-00-00-"+thread+".jsonl")
	line := func(msg string) string {
		return fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", thread) +
			fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":%q}}`, msg) + "\n"
	}
	require.NoError(t, os.WriteFile(rollout, []byte(line("the real conversation")), 0o600))
	require.NoError(t, transcriptlink.BindCodex(sid, thread))
	link := transcriptlink.Codex(sid)
	require.NotEmpty(t, link.Path, "the rollout is checked and believed")

	msgs, _, err := ReadFromContext(context.Background(), link.Path, "codex", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	require.NoError(t, os.WriteFile(outside, []byte(line("OUTSIDE-SECRET")), 0o600))

	// The file swapped for a link after the check.
	require.NoError(t, os.Remove(rollout))
	require.NoError(t, os.Symlink(outside, rollout))
	_, _, err = ReadFromContext(context.Background(), link.Path, "codex", 0)
	assert.Error(t, err, "the swapped-in link is refused at open time")
	_, n := NewSessionReader().ReadAllMessagesForLiveContext(context.Background(), sid, "", "codex")
	assert.Equal(t, 0, n, "the live reader gets nothing from the outside file")
	assert.NotContains(t, NewSessionReader().FirstUserPrompt(sid, "", "codex"), "OUTSIDE-SECRET")

	// The day's folder swapped for a link to an outside folder holding a same-named file.
	require.NoError(t, os.Remove(rollout))
	outDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outDir, filepath.Base(rollout)), []byte(line("OUTSIDE-SECRET")), 0o600))
	require.NoError(t, os.Remove(day))
	require.NoError(t, os.Symlink(outDir, day))
	_, _, err = ReadFromContext(context.Background(), link.Path, "codex", 0)
	assert.Error(t, err, "a symlinked folder on the way is refused too")
	_, n = NewSessionReader().ReadAllMessagesForLiveContext(context.Background(), sid, "", "codex")
	assert.Equal(t, 0, n)
}
