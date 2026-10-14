package tmux

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed tmux command's log line carries tmux's own explanation (QA, flake hunt).
func TestExecStderrExplainsAFailure(t *testing.T) {
	c := newTestClient(t)
	_, err := c.runOnSocket(context.Background(), c.SocketPath, "has-session", "-t", "no-such-session")
	require.Error(t, err)
	var ee *exec.ExitError
	require.True(t, errors.As(err, &ee))
	msg := execStderr(err)
	assert.NotEmpty(t, msg, "tmux said why")
	assert.NotContains(t, msg, "\n", "one line")
}

func TestExecStderrIsCapped(t *testing.T) {
	err := &exec.ExitError{Stderr: []byte(strings.Repeat("x ", 1000))}
	msg := execStderr(err)
	assert.LessOrEqual(t, len(msg), execStderrMax+len("…"))
	assert.Equal(t, "", execStderr(errors.New("not an exit error")))
}

// The cap never splits a multi-byte character (#3228 review).
func TestExecStderrCapKeepsCharactersWhole(t *testing.T) {
	err := &exec.ExitError{Stderr: []byte("x" + strings.Repeat("é", 400))}
	msg := execStderr(err)
	assert.True(t, utf8.ValidString(msg), "valid UTF-8: %q", msg[len(msg)-8:])
	assert.LessOrEqual(t, len(msg), execStderrMax+len("…"))
}
