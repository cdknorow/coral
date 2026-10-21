package tmux

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On tmux, a single-line automated text is typed with its control characters gone.
func TestAutomatedTypedSendIsSanitisedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	ctx := WithAutomated(context.Background())
	require.NoError(t, c.SendKeys(ctx, "agent", "hi\x1b[201~\x03 there", "claude", longTextSID))
	joined := strings.Join(readCalls(t, dir), "\n")
	assert.Contains(t, joined, "send-keys -t "+promptTestSession+":0.0 -l -- hi[201~ there")
	assert.NotContains(t, joined, "paste-buffer")
	assert.NotContains(t, joined, "\x1b")
}

// A multi-line one is flattened to one typed line ONLY when the pane is known not to take bracketed paste; unknown or on,
// it is pasted (paste-buffer -p), so a bracketed agent still gets its line breaks (QA's regression on #62).
func TestAutomatedMultiLineTextOnTmuxIsFlattenedOnlyWhenBracketedPasteIsKnownOff(t *testing.T) {
	skipWithoutUnixTmux(t)
	text := "first\x03 line\nsecond line"
	for name, tc := range map[string]struct {
		ctx          context.Context
		wantFlat     bool
		wantPasteCmd bool
	}{
		"unknown": {WithAutomated(context.Background()), false, true},
		"on":      {WithBracketedPasteHint(WithAutomated(context.Background()), true), false, true},
		"off":     {WithBracketedPasteHint(WithAutomated(context.Background()), false), true, false},
	} {
		c, dir := fakeTmux(t)
		require.NoError(t, c.SendKeys(tc.ctx, "agent", text, "claude", longTextSID), name)
		joined := strings.Join(readCalls(t, dir), "\n")
		assert.Equal(t, tc.wantPasteCmd, strings.Contains(joined, "paste-buffer"), name)
		assert.Equal(t, tc.wantFlat, strings.Contains(joined, "send-keys -t "+promptTestSession+":0.0 -l -- first line second line"), name)
	}
}

func TestAutomatedMultiLineTextOfOnlyControlCharactersIsRefusedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, _ := fakeTmux(t)
	err := c.SendKeys(WithAutomated(context.Background()), "agent", "\n\x03\n", "claude", longTextSID)
	assert.ErrorIs(t, err, ErrNothingToSend)
}
