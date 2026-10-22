package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// The one line "Agents now stop when you quit Coral" is owed once, to an install that ran on the old tmux default, at its first start on the built-in terminal default (#6064).

func noticeRig(t *testing.T, usedBefore bool) *store.SessionStore {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "n.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	if usedBefore {
		sid := "11111111-0000-4000-8000-000000000000"
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: "old", AgentType: "claude", WorkingDir: t.TempDir()}))
	}
	return ss
}

func noticeOf(t *testing.T, ss *store.SessionStore) string {
	t.Helper()
	s, err := ss.GetSettings(context.Background())
	require.NoError(t, err)
	return setreg.Raw(s, setreg.TerminalDefaultNotice)
}

func TestOwedDefaultNotice(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name          string
		used          bool
		saved         string
		backend, goos string
		forced        bool
		want          string
	}{
		{"an install that ran on the old default, first pty start", true, "", "pty", "darwin", false, "pending"},
		{"the same on Linux", true, "", "pty", "linux", false, "pending"},
		{"a new install is owed nothing", false, "", "pty", "darwin", false, "none"},
		{"this start kept tmux for running agents: decided later", true, "", "tmux", "darwin", false, ""},
		{"an explicit pty choice", true, "pty", "pty", "linux", false, ""},
		{"an explicit tmux choice", true, "tmux", "tmux", "linux", false, ""},
		{"--backend", true, "", "pty", "darwin", true, ""},
		{"Windows was always pty", true, "", "pty", "windows", false, ""},
	} {
		ss := noticeRig(t, c.used)
		owedDefaultNotice(ctx, ss, c.saved, c.backend, c.forced, c.goos)
		assert.Equal(t, c.want, noticeOf(t, ss), c.name)
	}
}

// Decided once, never changed by Coral: a dismissed line stays dismissed, a "none" never becomes "pending" when the install later runs agents, and the tmux start's undecided notice is decided
// at the next start that runs pty.
func TestOwedDefaultNoticeIsDecidedOnce(t *testing.T) {
	ctx := context.Background()
	ss := noticeRig(t, true)

	owedDefaultNotice(ctx, ss, "", "tmux", false, "linux") // the exception start
	assert.Equal(t, "", noticeOf(t, ss))
	owedDefaultNotice(ctx, ss, "", "pty", false, "linux") // the next start, with none left
	assert.Equal(t, "pending", noticeOf(t, ss))

	require.NoError(t, ss.SetSetting(ctx, setreg.TerminalDefaultNotice, "seen"))
	owedDefaultNotice(ctx, ss, "", "pty", false, "linux")
	assert.Equal(t, "seen", noticeOf(t, ss), "dismissed: never shown again")

	fresh := noticeRig(t, false)
	owedDefaultNotice(ctx, fresh, "", "pty", false, "linux")
	require.Equal(t, "none", noticeOf(t, fresh))
	require.NoError(t, fresh.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "22222222-0000-4000-8000-000000000000", AgentName: "later", AgentType: "claude", WorkingDir: t.TempDir()}))
	owedDefaultNotice(ctx, fresh, "", "pty", false, "linux")
	assert.Equal(t, "none", noticeOf(t, fresh), "a new install that later runs agents is not an old one")
}
