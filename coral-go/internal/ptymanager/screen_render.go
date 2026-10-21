package ptymanager

import (
	"context"

	"github.com/subgentic/coral/internal/vtscreen"
)

// A PTY session's replay ring is its raw output, escapes and cursor moves included. Claude Code draws
// line by line, but Codex (ratatui) places every row, and every word of a row, with cursor moves and never
// writes a newline, so no option line of its prompts could be read from that text: prompt_options came back
// empty for its hook-trust menu on the PTY backend (#4543). When no CLI signal says what is on screen, the
// prompt reader gets the screen as drawn, rendered by internal/vtscreen.

// RenderedScreenReader is implemented by terminals whose CaptureOutput is not the screen as drawn (the PTY
// backend's): the agent's visible screen as text, one line per row, for reading a prompt from it.
type RenderedScreenReader interface {
	RenderedScreen(ctx context.Context, name, agentType, sessionID string) (string, error)
}

// RenderScreen is a terminal's output rendered to its cols x rows screen (vtscreen.Render).
func RenderScreen(data []byte, cols, rows int) string {
	return vtscreen.Render(data, cols, rows)
}

// RenderedScreen implements RenderedScreenReader: the session's output (its replay ring) rendered at the
// size its PTY was last given.
func (p *PTYSessionTerminal) RenderedScreen(_ context.Context, name, _, sessionID string) (string, error) {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return "", nil // gone: nothing to show, and never another session's screen
	}
	data, err := p.backend.Replay(key)
	if err != nil {
		return "", err
	}
	cols, rows, err := p.backend.PaneSize(key)
	if err != nil {
		return "", err
	}
	return RenderScreen(data, cols, rows), nil
}

// AgentScreenStyled implements StyledScreenReader for the PTY backend: AgentScreen with each faint cell
// (SGR 2) marked as tmux's capture-pane -e marks it, so Claude's faint prompt suggestion is not read as a
// person's draft (#3468, F10).
func (p *PTYSessionTerminal) AgentScreenStyled(_ context.Context, name, _, sessionID string) (string, error) {
	key, err := p.keyFor(name, sessionID)
	if err != nil {
		return "", err
	}
	return p.backend.renderedScreenStyled(key)
}
