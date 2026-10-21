package inputlog

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// fakeToken is a GitHub token by its shape (ghp_ and 36 characters): the redactor's rules know it.
const fakeToken = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"

// #60 (Security's condition a): the sent text is checked for secrets in full, before the line is
// cut to maxText, so a secret that starts in the shown part and runs past the cut is still
// redacted; a paste and decoded hex bytes go through the same check.
func TestSecretsAreRedactedBeforeTheCut(t *testing.T) {
	path := enable(t)
	captureLog(t)
	split := strings.Repeat("a", maxText-10) + " " + fakeToken + " tail"
	Write("p", Text, split)
	Write("p", Paste, "token: "+fakeToken+"\n")
	var hexBytes []string // as tmux send-keys -H gets them
	for _, c := range []byte(fakeToken) {
		hexBytes = append(hexBytes, hex.EncodeToString([]byte{c}))
	}
	recordHexKeys("p", hexBytes)
	text := readFile(t, path)
	assert.NotContains(t, text, fakeToken)
	assert.NotContains(t, text, fakeToken[:20], "not even the part before the cut")
	assert.Equal(t, 3, strings.Count(text, "\n"), "every line is still logged: %s", text)
}

// #60 (Security's condition c): a redactor that fails or panics gives a line with no text, and
// the writer goes on.
func TestRedactionFailureFailsClosed(t *testing.T) {
	path := enable(t)
	captureLog(t)
	prev := redactText
	t.Cleanup(func() { redactText = prev })
	// Each line is written by the writer goroutine: Flush before the redactor changes.
	redactText = func(string) (string, error) { return "", errors.New("budget exceeded") }
	Write("p", Text, "secret-ish "+fakeToken)
	Flush()
	redactText = func(string) (string, error) { panic("redactor bug") }
	Write("p", Text, "more "+fakeToken)
	Flush()
	redactText = prev
	Write("p", Text, "after")
	text := readFile(t, path)
	assert.NotContains(t, text, fakeToken)
	assert.Equal(t, 2, strings.Count(text, "text=[not logged: redaction failed]"), text)
	assert.Contains(t, text, `text="after"`, "the writer survived the panic")
}

// #60: a log not written for retention is deleted at startup; turning the log off removes its
// rotated copy; an existing log is made private before anything is appended (Security's
// condition d: an older Coral may have created it with the folder's access list).
func TestRetentionAndPrivacyOfTheFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	captureLog(t)
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	require.NoError(t, os.WriteFile(path+".1", []byte("older\n"), 0o644))
	old := time.Now().Add(-8 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	require.NoError(t, os.Chtimes(path+".1", old, old))
	Setup(dir, false)
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	assert.NoFileExists(t, path, "unwritten for 8 days: deleted at startup")
	assert.NoFileExists(t, path+".1")

	// A recent, loose file is kept and made private when the log turns on.
	require.NoError(t, os.WriteFile(path, []byte("recent\n"), 0o644))
	require.NoError(t, os.WriteFile(path+".1", []byte("rotated\n"), 0o644))
	SetSetting(true)
	Write("p", Text, "x")
	Flush()
	secfiletest.AssertPrivate(t, path)
	secfiletest.AssertPrivate(t, path+".1")
	SetSetting(false)
	assert.NoFileExists(t, path+".1", "the rotated copy goes when the log is turned off")
	assert.FileExists(t, path, "the log stays until the person deletes it (or retention)")
}

// #60 review N1: an entry keeps at most redactWindow bytes of the sent text, so the writer's queue
// is bounded whatever is pasted; the redactor still sees well past the shown part.
func TestEntriesAreBounded(t *testing.T) {
	path := enable(t)
	captureLog(t)
	prev := redactText
	t.Cleanup(func() { redactText = prev })
	var seen []int
	redactText = func(s string) (string, error) { seen = append(seen, len(s)); return s, nil }
	Write("p", Paste, strings.Repeat("x", 1<<20))
	Flush()
	require.Len(t, seen, 1)
	assert.Equal(t, redactWindow, seen[0])
	assert.Greater(t, redactWindow, 4*maxText, "redaction sees well past the cut")
	assert.Contains(t, readFile(t, path), "len=4096")
}

// recordHexKeys logs hex bytes (one per argument) as the tmux send-keys -H call decodes them.
func recordHexKeys(target string, hexBytes []string) {
	b := make([]byte, 0, len(hexBytes))
	for _, h := range hexBytes {
		if v, err := strconv.ParseUint(h, 16, 8); err == nil {
			b = append(b, byte(v))
		}
	}
	Write(target, Raw, string(b))
}
