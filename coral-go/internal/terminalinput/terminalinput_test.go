package terminalinput

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
)

// #5706 (Security's condition 2): the zero value fails closed. Its text is made safe like Coral's, never left as it is.
func TestTheZeroOriginIsUnknownAndItsTextIsMadeSafe(t *testing.T) {
	var o Origin
	assert.Equal(t, Unknown, o)
	assert.Equal(t, "unknown", o.String())
	typed, err := PrepareTyped(o, "ls\x03 -la\nrm -rf /\x1b[201~")
	require.NoError(t, err)
	assert.Equal(t, "ls -la rm -rf /[201~", typed, "no key, no line break")
	_, err = PrepareTyped(o, "\x03\x1b")
	assert.ErrorIs(t, err, ErrNothingToSend)
	_, err = PreparePaste(o, "\x03\x1b")
	assert.ErrorIs(t, err, ErrNothingToSend, "nothing left of a paste is refused, not a bare Enter")
}

// A person's text is never changed by Prepare (a typed line), and a paste is cleaned for everyone, as before.
func TestPrepareForAPersonIsANoOp(t *testing.T) {
	for _, in := range []string{"plain", "a\x03b", "\x1b[A", "line\nbreak", "\x1b", "\u009b", ""} {
		for _, o := range []Origin{Person, Internal, Unclassified} {
			got, err := PrepareTyped(o, in)
			require.NoError(t, err)
			assert.Equal(t, in, got, "%s %q", o, in)
		}
	}
	body, err := PreparePaste(Person, "a\x1b[201~b\r\nc")
	require.NoError(t, err)
	assert.Equal(t, "a[201~b\nc", body, "a paste never carries the terminator or a CR, whoever sent it")
	body, err = PreparePaste(Person, "\x1b\x03")
	assert.NoError(t, err, "a person's paste of only controls is not refused")
	assert.Empty(t, body)
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	inputlog.Flush()
	b, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	return string(b)
}

// Record: a person's text, and the unknown origin's, by length only unless it is only control input; everything else as it is.
func TestRecordHidesAPersonsTextAndNobodyElses(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	Record(Person, "p", Raw, "hunter2")
	Record(Person, "p", Text, "pasted xyz")
	Record(Person, "p", Raw, "\r")
	Record(Person, "p", Raw, "\x1b[A")
	Record(Person, "p", Keys, "Enter")
	Record(Coral, "p", Text, "coral's own text")
	Record(Internal, "p", Raw, "\x03")
	Record(Unclassified, "p", Text, "unclassified text")
	Record(Unknown, "p", Text, "unknown text")
	got := readLog(t, dir)
	assert.NotContains(t, got, "hunter2")
	assert.NotContains(t, got, "pasted")
	assert.Contains(t, got, `kind=typed text="[typed 7 bytes]"`)
	assert.Contains(t, got, `kind=typed text="[typed 10 bytes]"`)
	assert.Contains(t, got, `kind=raw len=1 text="\r"`)
	assert.Contains(t, got, `kind=raw len=3 text="\x1b[A"`)
	assert.Contains(t, got, `keys="Enter"`)
	assert.Contains(t, got, `text="coral's own text"`)
	assert.Contains(t, got, `kind=raw len=1 text="\x03"`)
	assert.Contains(t, got, `text="unclassified text"`)
	assert.NotContains(t, got, "unknown text", "the unknown origin fails closed in the log too (#5715)")
	assert.Contains(t, got, `kind=typed text="[typed 12 bytes]"`)
}

// The debug log's source= is the code that sent the input, never terminalinput.Record: the package is part of the terminal plumbing the
// stack walk skips (#5706, Security's condition 5).
func TestSourceSkipsThisPackage(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	recordFromHelper()
	got := readLog(t, dir)
	assert.Contains(t, got, "source=terminalinput.TestSourceSkipsThisPackage ", got)
	assert.NotContains(t, got, "source=terminalinput.Record")
	assert.NotContains(t, got, "source=terminalinput.recordFromHelper")
}

// recordFromHelper records one line from a function of this package other than a test, to check that the debug log's source= walks past it.
func recordFromHelper() { Record(Coral, "p", Text, "from a helper") }
