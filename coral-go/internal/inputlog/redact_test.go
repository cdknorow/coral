package inputlog

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
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
	Record("p", Text, split)
	Record("p", Paste, "token: "+fakeToken+"\n")
	var hexBytes []string // as tmux send-keys -H gets them
	for _, c := range []byte(fakeToken) {
		hexBytes = append(hexBytes, hex.EncodeToString([]byte{c}))
	}
	RecordHexKeys("p", hexBytes)
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
	Record("p", Text, "secret-ish "+fakeToken)
	Flush()
	redactText = func(string) (string, error) { panic("redactor bug") }
	Record("p", Text, "more "+fakeToken)
	Flush()
	redactText = prev
	Record("p", Text, "after")
	text := readFile(t, path)
	assert.NotContains(t, text, fakeToken)
	assert.Equal(t, 2, strings.Count(text, "text=[not logged: redaction failed]"), text)
	assert.Contains(t, text, `text="after"`, "the writer survived the panic")
}

// #60 (Security's condition b): a person's own input from the dashboard terminal is logged by
// its length only, typed or pasted; Enter, arrows and Ctrl-C stay as they are.
func TestAPersonsOwnInputIsLoggedByLengthOnly(t *testing.T) {
	path := enable(t)
	captureLog(t)
	human := Human(context.Background())
	RecordCtx(human, "p", Raw, "hunter2")
	RecordCtx(human, "p", Text, "pasted "+fakeToken)
	RecordCtx(human, "p", Raw, "\r")
	RecordCtx(human, "p", Raw, "\x1b[A")
	RecordCtx(human, "p", Raw, "\x03")
	RecordCtx(human, "p", Keys, "Enter")
	RecordCtx(context.Background(), "p", Text, "coral's own text")
	text := readFile(t, path)
	assert.NotContains(t, text, "hunter2")
	assert.NotContains(t, text, "pasted")
	assert.Contains(t, text, `kind=typed text="[typed 7 bytes]"`)
	assert.Contains(t, text, `kind=raw len=1 text="\r"`)
	assert.Contains(t, text, `kind=raw len=3 text="\x1b[A"`)
	assert.Contains(t, text, `kind=raw len=1 text="\x03"`)
	assert.Contains(t, text, `keys="Enter"`)
	assert.Contains(t, text, `text="coral's own text"`)
}

func TestHasPrintable(t *testing.T) {
	for s, want := range map[string]bool{
		"a": true, "hunter2": true, " ": true, "\x1b[Ax": true, "é": true,
		"": false, "\r": false, "\x03": false, "\x1b[A": false, "\x1bOP": false, "\x1b[1;5C": false, "\x7f": false, "\t\r": false,
	} {
		assert.Equal(t, want, hasPrintable(s), "%q", s)
	}
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
	Record("p", Text, "x")
	Flush()
	secfiletest.AssertPrivate(t, path)
	secfiletest.AssertPrivate(t, path+".1")
	SetSetting(false)
	assert.NoFileExists(t, path+".1", "the rotated copy goes when the log is turned off")
	assert.FileExists(t, path, "the log stays until the person deletes it (or retention)")
}
