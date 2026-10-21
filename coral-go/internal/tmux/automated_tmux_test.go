package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// On tmux, a single-line automated text is typed with its control characters gone.
func TestAutomatedTypedSendIsSanitisedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, dir := fakeTmux(t)
	ctx := context.Background()
	require.NoError(t, c.SendKeys(ctx, terminalinput.Coral, "agent", "hi\x1b[201~\x03 there", "claude", longTextSID))
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
		"unknown": {context.Background(), false, true},
		"on":      {WithBracketedPasteHint(context.Background(), true), false, true},
		"off":     {WithBracketedPasteHint(context.Background(), false), true, false},
	} {
		c, dir := fakeTmux(t)
		require.NoError(t, c.SendKeys(tc.ctx, terminalinput.Coral, "agent", text, "claude", longTextSID), name)
		joined := strings.Join(readCalls(t, dir), "\n")
		assert.Equal(t, tc.wantPasteCmd, strings.Contains(joined, "paste-buffer"), name)
		assert.Equal(t, tc.wantFlat, strings.Contains(joined, "send-keys -t "+promptTestSession+":0.0 -l -- first line second line"), name)
	}
}

func TestAutomatedMultiLineTextOfOnlyControlCharactersIsRefusedOnTmux(t *testing.T) {
	skipWithoutUnixTmux(t)
	c, _ := fakeTmux(t)
	err := c.SendKeys(context.Background(), terminalinput.Coral, "agent", "\n\x03\n", "claude", longTextSID)
	assert.ErrorIs(t, err, terminalinput.ErrNothingToSend)
}

// Security's note on #62: with the bracketed-paste mode unknown, a pane whose foreground program is a plain shell is treated
// as "off": a shell never enabled bracketed paste and paste-buffer would turn each LF into Enter. Any other program, or a
// known "on", is still pasted.
func TestAutomatedMultiLineTextToAShellPaneIsFlattenedWhenTheModeIsUnknown(t *testing.T) {
	skipWithoutUnixTmux(t)
	text := "echo one\necho two"
	for name, tc := range map[string]struct {
		fg       string
		ctx      context.Context
		wantFlat bool
	}{
		"bash, unknown":     {"bash\n", context.Background(), true},
		"pwsh, unknown":     {"pwsh\n", context.Background(), true},
		"-zsh, unknown":     {"-zsh\n", context.Background(), true},
		"claude, unknown":   {"claude\n", context.Background(), false},
		"node, unknown":     {"node\n", context.Background(), false},
		"unreadable":        {"", context.Background(), false},
		"bash, known on":    {"bash\n", WithBracketedPasteHint(context.Background(), true), false},
		"claude, known off": {"claude\n", WithBracketedPasteHint(context.Background(), false), true},
	} {
		c, dir := fakeTmux(t)
		if tc.fg != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "fgcmd"), []byte(tc.fg), 0o600))
		}
		require.NoError(t, c.SendKeys(tc.ctx, terminalinput.Coral, "agent", text, "claude", longTextSID), name)
		joined := strings.Join(readCalls(t, dir), "\n")
		assert.Equal(t, tc.wantFlat, strings.Contains(joined, "send-keys -t "+promptTestSession+":0.0 -l -- echo one echo two"), name)
		assert.Equal(t, !tc.wantFlat, strings.Contains(joined, "paste-buffer"), name)
	}
}
