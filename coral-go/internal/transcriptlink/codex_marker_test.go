package transcriptlink

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6325: with no link (Codex's hooks not trusted, so none wrote one) a wake finds the session's own thread by the Coral marker in the rollout's first developer message, and binds it.
const (
	markSession = "3f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	markOther   = "4f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5c"
	markThread  = "0ccccccc-cccc-7ccc-8ccc-cccccccccccc"
	markThread2 = "0ddddddd-dddd-7ddd-8ddd-dddddddddddd"
)

// dayRollout writes thread's rollout under the day folder `ago` days back; body is the second record ("" for none).
func dayRollout(t *testing.T, home, thread string, ago int, hour, body string) {
	t.Helper()
	day := time.Now().AddDate(0, 0, -ago)
	dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	rec := `{"type":"session_meta","payload":{"id":"` + thread + `"}}` + "\n"
	if body != "" {
		rec += body + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-"+day.Format("2006-01-02")+"T"+hour+"-"+thread+".jsonl"), []byte(rec), 0o600))
}

func setMarkerSearch(t *testing.T, days, files int, within time.Duration) {
	t.Helper()
	d, f, w := markerSearchDays, markerSearchFiles, markerSearchTime
	markerSearchDays, markerSearchFiles, markerSearchTime = days, files, within
	t.Cleanup(func() { markerSearchDays, markerSearchFiles, markerSearchTime = d, f, w })
}

func TestBindCodexByMarker(t *testing.T) {
	t.Run("an unlinked session is bound to the rollout that carries its marker", func(t *testing.T) {
		home := codexHomeFor(t)
		dayRollout(t, home, markThread, 1, "09-00-00", developerMarker(markSession))
		dayRollout(t, home, markThread2, 0, "10-00-00", developerMarker(markOther))
		require.True(t, BindCodexByMarker(markSession))
		link := Codex(markSession)
		assert.Equal(t, markThread, link.ThreadID)
		assert.NotEmpty(t, link.Path, "the link names the rollout file")
		assert.Empty(t, Codex(markOther).ThreadID, "another session's rollout is not bound by this search")
	})
	t.Run("a session that is linked keeps its link and nothing is searched", func(t *testing.T) {
		home := codexHomeFor(t)
		dayRollout(t, home, markThread, 1, "09-00-00", developerMarker(markSession))
		require.NoError(t, BindCodex(markSession, markThread))
		dayRollout(t, home, markThread2, 0, "10-00-00", developerMarker(markSession)) // a newer rollout with the same marker
		require.True(t, BindCodexByMarker(markSession))
		assert.Equal(t, markThread, Codex(markSession).ThreadID)
	})
	t.Run("no marker: nothing is found and nothing is guessed from the folder or the time", func(t *testing.T) {
		home := codexHomeFor(t)
		dayRollout(t, home, markThread, 0, "09-00-00", "") // a rollout of today with no marker
		assert.False(t, BindCodexByMarker(markSession))
		assert.Empty(t, Codex(markSession).ThreadID)
	})
	t.Run("another session's marker is refused", func(t *testing.T) {
		home := codexHomeFor(t)
		dayRollout(t, home, markThread, 0, "09-00-00", developerMarker(markOther))
		assert.False(t, BindCodexByMarker(markSession))
		assert.Empty(t, Codex(markSession).ThreadID)
	})
	t.Run("a marker quoted by the user or the assistant names nothing", func(t *testing.T) {
		home := codexHomeFor(t)
		quoted := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"CORAL_SESSION_ID: ` + markSession + `"}]}}`
		dayRollout(t, home, markThread, 0, "09-00-00", quoted)
		assert.False(t, BindCodexByMarker(markSession))
	})
	t.Run("not a session id: never searched", func(t *testing.T) {
		codexHomeFor(t)
		for _, id := range []string{"", "*", "../x", markSession + "*"} {
			assert.False(t, BindCodexByMarker(id), "%q", id)
		}
	})
	t.Run("a rollout older than the window is not searched", func(t *testing.T) {
		home := codexHomeFor(t)
		dayRollout(t, home, markThread, 30, "09-00-00", developerMarker(markSession))
		assert.False(t, BindCodexByMarker(markSession), "30 days back is outside the 14 days")
		dayRollout(t, home, markThread2, 10, "09-00-00", developerMarker(markSession))
		assert.True(t, BindCodexByMarker(markSession), "10 days back is inside")
		assert.Equal(t, markThread2, Codex(markSession).ThreadID)
	})
	t.Run("it gives up after the file limit", func(t *testing.T) {
		home := codexHomeFor(t)
		setMarkerSearch(t, 14, 2, 5*time.Second)
		dayRollout(t, home, markThread, 3, "09-00-00", developerMarker(markSession)) // the oldest of the three
		dayRollout(t, home, "0eeeeeee-eeee-7eee-8eee-eeeeeeeeeeee", 1, "09-00-00", "")
		dayRollout(t, home, "0fffffff-ffff-7fff-8fff-ffffffffffff", 0, "09-00-00", "")
		assert.False(t, BindCodexByMarker(markSession), "two files looked at, the third never reached")
		setMarkerSearch(t, 14, 3, 5*time.Second)
		assert.True(t, BindCodexByMarker(markSession))
	})
	t.Run("it gives up after the time limit", func(t *testing.T) {
		home := codexHomeFor(t)
		setMarkerSearch(t, 14, 500, time.Nanosecond)
		dayRollout(t, home, markThread, 0, "09-00-00", developerMarker(markSession))
		time.Sleep(2 * time.Millisecond) // sleep-ok: past a one-nanosecond deadline
		assert.False(t, BindCodexByMarker(markSession))
	})
}
