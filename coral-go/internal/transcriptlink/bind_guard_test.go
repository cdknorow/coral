package transcriptlink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	guardSession = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	otherSession = "2f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5c"
	threadA      = "0aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	threadB      = "0bbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"
)

// guardRollout writes a rollout for thread; line is the second record ("" for none).
func guardRollout(t *testing.T, home, thread, line string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "18")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := `{"type":"session_meta","payload":{"id":"` + thread + `"}}` + "\n"
	if line != "" {
		body += line + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-10-18T09-00-00-"+thread+".jsonl"), []byte(body), 0o600))
}

func developerMarker(session string) string {
	return `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\nCORAL_SESSION_ID: ` + session + `"}]}}`
}

func linkedThread(t *testing.T) string { t.Helper(); return Codex(guardSession).ThreadID }

// #4590: a session that already has a link to a live rollout keeps it unless the new rollout is
// marked as this session's own; a first bind is unchanged.
func TestBindCodex_AnExistingLinkIsKeptUnlessTheNewRolloutIsMarkedAsOurs(t *testing.T) {
	home := codexHomeFor(t)
	guardRollout(t, home, threadA, developerMarker(guardSession))

	require.NoError(t, BindCodex(guardSession, threadA), "a first bind needs no marker")
	assert.Equal(t, threadA, linkedThread(t))

	// Another Codex (started from this one's shell) names its own thread: refused when its
	// rollout is marked for another session, or for none.
	guardRollout(t, home, threadB, developerMarker(otherSession))
	assert.Error(t, BindCodex(guardSession, threadB))
	assert.Equal(t, threadA, linkedThread(t), "a rollout marked for another session does not take the link")
	guardRollout(t, home, threadB, "")
	assert.Error(t, BindCodex(guardSession, threadB))
	assert.Equal(t, threadA, linkedThread(t), "an unmarked rollout does not take it either")
	// A marker only counts in a developer message: quoted in a user message it names nothing.
	guardRollout(t, home, threadB, strings.Replace(developerMarker(guardSession), `"developer"`, `"user"`, 1))
	assert.Error(t, BindCodex(guardSession, threadB))
	assert.Equal(t, threadA, linkedThread(t))

	// A restart of this session: the new thread's rollout carries this session's id.
	guardRollout(t, home, threadB, developerMarker(guardSession))
	require.NoError(t, BindCodex(guardSession, threadB))
	assert.Equal(t, threadB, linkedThread(t))
}

func TestBindCodex_AVanishedRolloutDoesNotHoldTheLink(t *testing.T) {
	home := codexHomeFor(t)
	guardRollout(t, home, threadA, developerMarker(guardSession))
	require.NoError(t, BindCodex(guardSession, threadA))
	require.NoError(t, os.Remove(Codex(guardSession).Path))

	guardRollout(t, home, threadB, "") // unmarked, but the old rollout is gone
	require.NoError(t, BindCodex(guardSession, threadB))
	assert.Equal(t, threadB, linkedThread(t))
}

func TestIsSessionID(t *testing.T) {
	assert.True(t, IsSessionID(guardSession))
	for _, bad := range []string{"", "*", "?", "[a-z]", "1f2e3d4c", guardSession[:20], strings.ToUpper(guardSession), guardSession + "*", "../" + guardSession} {
		assert.False(t, IsSessionID(bad), "%q", bad)
	}
}
