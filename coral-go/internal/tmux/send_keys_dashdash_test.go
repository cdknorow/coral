package tmux

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3930 (Security's #3927 finding 5): every key goes to send-keys after "--", so a key from the
// Keys route that starts with "-" (-X, -F, -H) is a key, never a tmux option.
func TestSendKeysPutsKeysAfterDashDash(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	ctx := context.Background()
	require.NoError(t, c.SendRawKeys(ctx, terminalinput.Unclassified, promptTestSession, []string{"-X", "-F", "Enter"}, "claude", strings.TrimPrefix(promptTestSession, "claude-")))
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, promptTestSession+":0.0", "\r"))
	var sends []string
	for _, call := range readCalls(t, dir) {
		if strings.HasPrefix(call, "send-keys") {
			sends = append(sends, call)
		}
	}
	require.Len(t, sends, 4, "%v", sends)
	for _, call := range sends {
		assert.Contains(t, call, " -- ", call)
	}
	assert.True(t, strings.HasSuffix(sends[0], "-- -X"), sends[0])
	assert.True(t, strings.HasSuffix(sends[3], "-- Enter"), sends[3])
}
