//go:build !windows

package ptymanager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
)

// The input gate reads PTYSessionTerminal.AgentScreen, and the dashboard's styled check AgentScreenStyled: after the replay ring wrapped, both must show the composer drawn once (#6481).
func TestAgentScreenKeepsTheInputBoxAfterTheReplayRingWrapped(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	const sid = "6481aaaa-0000-4000-8000-000000000001"
	name := naming.SessionName("codex", sid)
	require.NoError(t, backend.Spawn(name, "codex", t.TempDir(), sid, codexLikeStandIn, 120, 40))
	ch, err := backend.Attach(name, "ws-agent-screen")
	require.NoError(t, err)
	var seen strings.Builder
	deadline := time.After(30 * time.Second)
	for !strings.Contains(seen.String(), "REPAINTS-DONE") {
		select {
		case data, ok := <-ch:
			require.True(t, ok, "the terminal closed")
			seen.Write(data)
			if seen.Len() > 4096 {
				s := seen.String()
				seen.Reset()
				seen.WriteString(s[len(s)-2048:])
			}
		case <-deadline:
			t.Fatal("the stand-in did not finish")
		}
	}
	replay, err := backend.Replay(name)
	require.NoError(t, err)
	require.NotContains(t, string(replay), "Ask Codex to do anything")

	term := NewPTYSessionTerminal(backend)
	screen, err := term.AgentScreen(context.Background(), name, "codex", sid)
	require.NoError(t, err)
	assert.True(t, InputBoxOnScreen(screen), "AgentScreen:\n%s", screen)
	rendered, err := term.RenderedScreen(context.Background(), name, "codex", sid)
	require.NoError(t, err)
	assert.Contains(t, rendered, "Ask Codex to do anything")
	styled, err := term.AgentScreenStyled(context.Background(), name, "codex", sid)
	require.NoError(t, err)
	assert.Contains(t, styled, "Ask Codex to do anything", "AgentScreenStyled")
	text, err := backend.RenderedScreenStyledText(name)
	require.NoError(t, err)
	assert.Contains(t, text, "Ask Codex to do anything", "RenderedScreenStyledText")
}
