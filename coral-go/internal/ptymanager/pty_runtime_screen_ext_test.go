package ptymanager_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
	"github.com/subgentic/coral/internal/tmux"
)

// A Codex-style frame: every row and word placed by cursor moves, no newline.
const codexFrame = "\x1b[1;1H\x1b[J\x1b[2;3HHooks need review" +
	"\x1b[4;1H› 1. Review hooks\x1b[5;3H2.\x1b[5;6HTrust\x1b[5;12Hall\x1b[5;16Hand\x1b[5;20Hcontinue" +
	"\x1b[4;1H  1. Review hooks\x1b[5;1H› 2. Trust all and continue"

// F5: the board, scheduler and nudge senders (background.PTYRuntime) read the rendered screen, as
// the HTTP senders do, so a Codex menu is seen.
func TestBackgroundPTYRuntimeReadsTheRenderedScreen(t *testing.T) {
	b, _ := ptymanager.NewFakeBackendForTest(t, "codex-5", codexFrame, 60, 8)
	rt := background.NewPTYRuntime(b)
	screen, err := rt.AgentScreen(context.Background(), "codex-5", "codex", "")
	require.NoError(t, err)
	assert.Contains(t, strings.Split(screen, "\n"), "› 2. Trust all and continue")
	_, ok := any(rt).(ptymanager.RenderedScreen)
	assert.True(t, ok, "the gate reads the runtime's box")
	open, _ := ptymanager.PromptOnScreen(screen)
	assert.True(t, open, "the menu is seen as an open prompt")
}

// F5/F4: the runtime's typed send honours the gate's Enter check.
func TestBackgroundPTYRuntimeSendInputWithholdsEnter(t *testing.T) {
	b, written := ptymanager.NewFakeBackendForTest(t, "claude-5b", "", 80, 24)
	rt := background.NewPTYRuntime(b)
	ctx := tmux.WithBeforeEnter(context.Background(), func(context.Context) bool { return false })
	err := rt.SendInput(ctx, terminalinput.Unclassified, "claude-5b", "nudge")
	assert.True(t, errors.Is(err, tmux.ErrEnterWithheld), "got %v", err)
	for _, w := range written() {
		assert.NotContains(t, string(w), "\r")
	}
}
