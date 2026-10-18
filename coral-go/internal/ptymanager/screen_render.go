package ptymanager

import (
	"context"
	"regexp"
	"strings"

	"github.com/hinshun/vt10x"
)

// A PTY session's CaptureOutput is its raw output, escapes and cursor moves included. Claude Code draws
// line by line, but Codex (ratatui) places every row, and every word of a row, with cursor moves and never
// writes a newline, so no option line of its prompts could be read from that text: prompt_options came back
// empty for its hook-trust menu on the PTY backend (#4543). The prompt reader needs the screen as drawn:
// the output is played through a terminal emulator of the session's size and its rows are read.

// RenderedScreenReader is implemented by terminals whose CaptureOutput is not the screen as drawn (the PTY
// backend's): the agent's visible screen as text, one line per row, for reading a prompt from it.
type RenderedScreenReader interface {
	RenderedScreen(ctx context.Context, name, agentType, sessionID string) (string, error)
}

// Used when the session's size is unknown (it has always been given one at spawn).
const defaultRenderCols, defaultRenderRows = 120, 40

// Sequences that ask the terminal something or set its keyboard protocol, never draw, and that vt10x would
// misread: a CSI with a private marker other than a DEC mode set or reset ("ESC [ ? u", the kitty keyboard
// query Claude Code sends at launch, vt10x takes for "restore the cursor", and the next screen was drawn over
// the welcome banner), a CSI with intermediate bytes ("ESC [ ? 2026 $ p", "ESC [ 0 SP q"), and APC, PM and
// SOS strings (Claude's kitty graphics query, whose payload vt10x would print).
var (
	terminalQueryRe  = regexp.MustCompile("\x1b\\[([?<=>])([0-?]*)([ -/]*)([@-~])|\x1b\\[[0-?]*[ -/]+[@-~]")
	terminalStringRe = regexp.MustCompile("\x1b[_^X][^\x1b\x07]*(?:\x1b\\\\|\x07)")
)

// dropTerminalQueries removes the sequences terminalQueryRe and terminalStringRe match from a terminal's
// output, keeping DEC private mode sets and resets ("ESC [ ? 25 h", "ESC [ ? 1049 h").
func dropTerminalQueries(data []byte) []byte {
	data = terminalStringRe.ReplaceAll(data, nil)
	return terminalQueryRe.ReplaceAllFunc(data, func(m []byte) []byte {
		sub := terminalQueryRe.FindSubmatch(m)
		if sub[1] != nil && string(sub[1]) == "?" && len(sub[3]) == 0 && (sub[4][0] == 'h' || sub[4][0] == 'l') {
			return m
		}
		return nil
	})
}

// RenderScreen plays a terminal's output through an emulator cols x rows and returns its visible rows, each
// with trailing blanks trimmed. Styling is dropped; what a row shows is kept.
func RenderScreen(data []byte, cols, rows int) string {
	if cols <= 0 || rows <= 0 {
		cols, rows = defaultRenderCols, defaultRenderRows
	}
	t := vt10x.New(vt10x.WithSize(cols, rows))
	_, _ = t.Write(dropTerminalQueries(data)) // replies the output asks for (cursor position, colours) go nowhere
	lines := strings.Split(strings.TrimSuffix(t.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
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
