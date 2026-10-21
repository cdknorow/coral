package inputlog

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// captureLog collects what goes to the standard log (coral.log in the server).
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	return &buf
}

func enable(t *testing.T) string {
	dir := t.TempDir()
	Setup(dir, true)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	return filepath.Join(dir, FileName)
}

func readFile(t *testing.T, path string) string {
	Flush()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// Off by default: nothing is recorded and no file is created, not even for the
// keys that end a CLI (operator decision, #3256).
func TestOffByDefaultRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	Setup(dir, false)
	SetSetting(false)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	out := captureLog(t)
	for _, c := range []struct {
		kind Kind
		data string
	}{
		{Text, "hello"}, {Keys, "C-c"}, {Keys, "Escape"}, {Raw, "\x04"}, {Text, "/exit"},
	} {
		Write("claude-x", c.kind, c.data)
	}
	Flush()
	_, err := os.Stat(filepath.Join(dir, FileName))
	assert.True(t, os.IsNotExist(err), "no file while off")
	assert.Empty(t, out.String(), "nothing in coral.log while off")
	assert.False(t, State().Enabled)
}

// The setting turns it on and off at once, and coral.log says so each time.
func TestSettingTogglesLive(t *testing.T) {
	dir := t.TempDir()
	Setup(dir, false)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	out := captureLog(t)

	SetSetting(true)
	assert.True(t, State().Enabled)
	assert.Contains(t, out.String(), "terminal input debug log is ON (the Log terminal input setting)")
	assert.Contains(t, out.String(), "with the secrets Coral recognises redacted")
	Write("p", Text, "while on")

	SetSetting(false)
	assert.False(t, State().Enabled)
	assert.Contains(t, out.String(), "terminal input debug log is OFF")
	Write("p", Text, "while off")

	got := readFile(t, filepath.Join(dir, FileName))
	assert.Contains(t, got, "while on")
	assert.NotContains(t, got, "while off")
}

// CORAL_DEBUG_INPUT / --debug-input force it on; the setting can't turn that off.
func TestForcedOnIgnoresTheSetting(t *testing.T) {
	dir := t.TempDir()
	out := captureLog(t)
	Setup(dir, true)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	SetSetting(false)
	st := State()
	assert.True(t, st.Enabled)
	assert.True(t, st.Forced)
	assert.Equal(t, filepath.Join(dir, FileName), st.Path)
	assert.Contains(t, out.String(), "ON (CORAL_DEBUG_INPUT / --debug-input)")
}

func TestDebugFileIsPrivate(t *testing.T) {
	path := enable(t)
	captureLog(t)
	Write("p", Text, "secret-ish")
	readFile(t, path)
	secfiletest.AssertPrivate(t, path)
}

// With the debug file on, every write is a line: target, source, kind, escaped and
// truncated text.
func TestDebugFileRecordsEveryWrite(t *testing.T) {
	path := enable(t)
	captureLog(t)
	Write("claude-a:0.0", Text, "echo hi")
	Write("claude-a:0.0", Keys, "Enter")
	Write("claude-a", Raw, "\x1b[A")
	Write("claude-a", Paste, strings.Repeat("x", 250)+"\n")
	got := readFile(t, path)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], `target=claude-a:0.0 source=inputlog.TestDebugFileRecordsEveryWrite kind=text len=7 text="echo hi"`)
	assert.Contains(t, lines[1], `kind=keys keys="Enter"`)
	assert.Contains(t, lines[2], `kind=raw len=3 text="\x1b[A"`)
	assert.Contains(t, lines[3], `kind=paste len=251 text="`+strings.Repeat("x", 200)+`" (+51 bytes)`)
}

func TestHexKeysAreDecoded(t *testing.T) {
	path := enable(t)
	captureLog(t)
	recordHexKeys("p", []string{"1b", "5b", "41"})
	assert.Contains(t, readFile(t, path), `kind=raw len=3 text="\x1b[A"`)
}

// The file is capped: at the cap it moves to .1 and a new file starts.
func TestDebugFileIsCapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	fw := newFileWriter(path, 1000, 256)
	for i := 0; i < 100; i++ {
		fw.add(entry{raw: fmt.Sprintf("line %03d %s\n", i, strings.Repeat("y", 40))})
	}
	fw.flush()
	cur, err := os.Stat(path)
	require.NoError(t, err)
	old, err := os.Stat(path + ".1")
	require.NoError(t, err)
	assert.LessOrEqual(t, cur.Size(), int64(1000))
	assert.LessOrEqual(t, old.Size(), int64(1000))
	b, _ := os.ReadFile(path)
	assert.Contains(t, string(b), "line 099", "the newest lines are in the current file")
	_, err = os.Stat(path + ".2")
	assert.True(t, os.IsNotExist(err), "only one old copy is kept")
}

// A slow or stuck file never holds up the sender: lines are dropped and counted.
//
// The writer goroutine reports and resets the drop counter (Swap) when it takes its next line, and when it first runs is up to the
// scheduler: on a loaded machine it can take the first line after most of the adds, and the counter is then already 0 here (#4992:
// "0 is not greater than 0"). So the test does not read the counter at one moment: every line must end up written to the file or
// counted as dropped (in a "line(s) dropped" report or the counter), whatever the order, and while the file is stuck at most the
// queue (4) and the line in the writer's hand (1) can have been kept.
func TestDebugFileNeverBlocksTheSender(t *testing.T) {
	const depth, adds = 4, 10000
	fw := newFileWriter(filepath.Join(t.TempDir(), FileName), 1<<20, depth)
	fw.mu.Lock() // the writer goroutine can't write: the queue fills
	done := make(chan struct{})
	go func() {
		for i := 0; i < adds; i++ {
			fw.add(entry{raw: "x\n"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("adding lines blocked on a stuck file")
	}
	fw.mu.Unlock()
	fw.flush() // the queue drains once the file writes again
	fw.add(entry{raw: "after\n"})
	fw.flush()
	b, _ := os.ReadFile(fw.path)
	text := string(b)
	assert.Contains(t, text, "line(s) dropped")
	assert.Contains(t, text, "after")

	kept := strings.Count(text, "x\n")
	reported := 0
	for _, m := range regexp.MustCompile(`(\d+) line\(s\) dropped`).FindAllStringSubmatch(text, -1) {
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		reported += n
	}
	left := int(fw.dropped.Load()) // counted after the last report
	assert.Equal(t, adds, kept+reported+left, "every line was written or counted as dropped (kept %d, reported %d, counted %d)", kept, reported, left)
	assert.LessOrEqual(t, kept, depth+1, "a stuck file keeps at most the queue and the line in the writer's hand")
	assert.Greater(t, reported+left, adds-depth-2, "the rest were dropped")
}

func TestEnabledFromEnv(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "True": true, "on": true, "": false, "0": false, "no": false, "False": false} {
		t.Setenv("CORAL_DEBUG_INPUT", v)
		assert.Equal(t, want, EnabledFromEnv(), "CORAL_DEBUG_INPUT=%q", v)
	}
}

// Turning the log off keeps the file and says so; Delete removes it and its rotated
// copy, but never while the log is on (#3256 review).
func TestDeleteKeptLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	out := captureLog(t)
	Setup(dir, false)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	SetSetting(true)
	Write("p", Text, "x")
	Flush()
	require.NoError(t, os.WriteFile(path+".1", []byte("old\n"), 0o600))

	_, err := Delete()
	assert.ErrorIs(t, err, ErrOn, "never while on")

	SetSetting(false)
	assert.Contains(t, out.String(), path+" is kept, its older copy (.1) removed")
	assert.NoFileExists(t, path+".1", "the rotated copy goes when the log is turned off (#60)")
	st := State()
	assert.True(t, st.FileExists)
	assert.Greater(t, st.FileBytes, int64(0))

	n, err := Delete()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.False(t, State().FileExists)
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

// The log never writes through a link planted at its path (a symbolic link; on Windows also a
// junction: #3920, on Windows it did), and Delete never removes one (#3256 review).
func TestLogFileIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	captureLog(t)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	linktest.Each(t, path, func(t *testing.T, p linktest.Planted) {
		Setup(dir, true)
		Write("p", Text, "must not land in the link target")
		Flush()
		Setup(dir, false)
		_, err := Delete()
		require.NoError(t, err)
		if p.Kind != "hardlink" { // removing a second name leaves the file it names as it was
			_, err = os.Lstat(path)
			assert.NoError(t, err, "Delete leaves the link alone")
		}
	})
}

// An existing log file with looser permissions is made 0600 (#3256 review).
func TestLogFileIsMadePrivate(t *testing.T) {
	captureLog(t)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	dir2 := t.TempDir()
	path2 := filepath.Join(dir2, FileName)
	require.NoError(t, os.WriteFile(path2, []byte("earlier\n"), 0o644))
	secfiletest.Expose(t, path2)
	Setup(dir2, true)
	Write("p", Text, "now")
	Flush()
	secfiletest.AssertPrivate(t, path2) // an existing looser file is made private
}

// #5706 (Security's condition 6): a record is a non-blocking enqueue. A writer that takes no more lines (a stuck disk) fills its queue
// and the next lines are dropped and counted; Write never waits, so the Windows stop's second Ctrl-C and exit line, which are recorded,
// go out as on time as without a log.
func TestWriteNeverWaitsForAStuckWriter(t *testing.T) {
	fw := &fileWriter{ch: make(chan entry, 4)} // no loop reads it
	prev := w.Load()
	w.Store(fw)
	enabled.Store(true)
	t.Cleanup(func() { enabled.Store(false); w.Store(prev) })
	start := time.Now()
	for i := 0; i < 1000; i++ {
		Write("p", Text, "x")
	}
	assert.Less(t, time.Since(start), time.Second, "1000 writes into a full queue return at once")
	assert.Equal(t, int64(996), fw.dropped.Load(), "what did not fit is dropped and counted")
}

// #5706 (Security's condition 4): a note goes through the same redaction and cut as every record, never raw: the [HELD ...] text carries a
// board message's text, which may hold a token.
func TestANoteIsRedactedAndCutLikeAnyRecord(t *testing.T) {
	path := enable(t)
	captureLog(t)
	Note("claude-x", Text, "[HELD, a prompt is open] please use "+fakeToken+" for the call")
	Note("claude-x", Keys, "[auto-accept answers an approval prompt]")
	Note("claude-x", Text, "[HELD, why] "+strings.Repeat("y", 400))
	text := readFile(t, path)
	assert.NotContains(t, text, fakeToken, "a token in a note is masked")
	assert.NotContains(t, text, fakeToken[:20])
	assert.Contains(t, text, `[HELD, a prompt is open] please use`)
	assert.Contains(t, text, `kind=keys keys="[auto-accept answers an approval prompt]"`)
	assert.Contains(t, text, "(+", "a long note is cut like a long record")
}
