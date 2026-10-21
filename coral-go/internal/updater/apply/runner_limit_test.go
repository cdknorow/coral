package apply

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A process list as big as a busy Mac's (about 500 KB) is read whole: with the old 64 KB cap the new Coral was not in it (#5581).
func TestLimitedKeepsAnAnswerAsLongAsAProcessList(t *testing.T) {
	var l limited
	line := strings.Repeat("x", 120) + "\n"
	for i := 0; i < 5000; i++ { // 600 KB
		n, err := l.Write([]byte(line))
		require.NoError(t, err)
		require.Equal(t, len(line), n)
	}
	assert.False(t, l.truncated)
	assert.Equal(t, 5000*len(line), l.b.Len())
}

func TestLimitedNotesATruncationAndStillReportsTheFullWrite(t *testing.T) {
	var l limited
	big := make([]byte, maxToolOutput+1000)
	n, err := l.Write(big)
	require.NoError(t, err)
	assert.Equal(t, len(big), n, "a short write would make exec kill the tool")
	assert.True(t, l.truncated)
	assert.Equal(t, maxToolOutput, l.b.Len())
	n, err = l.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, maxToolOutput, l.b.Len())
}

// A tool whose answer was cut fails the call: a truncated process list must never read as "Coral is not running".
func TestRunnerFailsClosedOnAnAnswerThatWasCut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/dd")
	}
	r := NewRunner()
	out, err := r.Run(context.Background(), "/bin/dd", "if=/dev/zero", "bs=1048576", "count=17", "status=none")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "longer than")
	assert.Empty(t, out)
	out, err = r.Run(context.Background(), "/bin/echo", "ok")
	require.NoError(t, err)
	assert.Equal(t, "ok\n", out)
}
