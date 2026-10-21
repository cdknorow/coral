// Package vtscreen renders a terminal's output to the text of its screen, for reading an agent's prompt on
// the PTY backend when the agent CLI gives no signal for it (#4543: Codex's hook review and Claude Code's
// trust menu come before any hook runs). The CLIs' own events come first; this is the fallback.
//
// The output is what the agent printed, so a hostile or prompt-injected model controls every byte, and it
// is replayed from the session's ring on every read. A general terminal emulator spun for minutes on 8 bytes
// and panicked on negative parameters (Security on #4543), so this is deliberately tiny: printable UTF-8
// text, carriage return, line feed (scrolling the screen at its bottom), backspace, tab to the next stop,
// cursor addressing (CUP, CUU, CUD, CUF, CUB, CNL, CPL, CHA, VPA) and erase (ED, EL, ECH). Styling and every other
// sequence are skipped, never interpreted: no repeat, insert, delete or scroll-region operation exists here.
// Parameters are capped as they are read and clamped to the screen, the screen and the input are capped,
// the work is counted against a budget past which nothing more is drawn, and a panic (a bug) is recovered
// as an empty screen.
package vtscreen

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const (
	// Used when the size is unknown.
	DefaultCols, DefaultRows = 120, 40
	// The largest screen rendered; a bigger pane is read at this size.
	MaxCols, MaxRows = 500, 200
	// Only the end of the output is played: the screen it leaves is what a prompt reader wants.
	MaxInput = 512 << 10
	// The work allowed per render, in cell operations (a cell written or cleared, a row scrolled counts its
	// cells): a full input of ordinary output costs a few million; one of clear-screens stops here, well
	// under a second.
	Budget = 40_000_000
	// Parameters past this many are dropped, and each is capped while it is read.
	maxParams, maxParam = 16, 9999
)

// recovered counts the renders that panicked: a bug, and the tests require it to stay 0.
var recovered atomic.Int64

// RenderStyled is Render with each faint cell (SGR 2) marked in the text as the SGR sequences tmux's
// capture-pane -e prints (ESC[2m ... ESC[22m... per row), so a reader of the styled screen can tell Claude's
// faint prompt suggestion from a person's text (F10). No other styling is kept.
func RenderStyled(output []byte, cols, rows int) (screen string) {
	return render(output, cols, rows, true)
}

// Render plays output through a cols x rows screen and returns its rows, each with trailing blanks trimmed.
func Render(output []byte, cols, rows int) (screen string) { return render(output, cols, rows, false) }

func render(output []byte, cols, rows int, styled bool) (screen string) {
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	cols, rows = min(cols, MaxCols), min(rows, MaxRows)
	if len(output) > MaxInput {
		output = output[len(output)-MaxInput:]
	}
	defer func() {
		if r := recover(); r != nil {
			recovered.Add(1)
			slog.Warn("vtscreen: recovered a panic, read as an empty screen", "panic", r)
			screen = strings.Repeat("\n", rows-1)
		}
	}()
	s := newTerm(cols, rows)
	s.write(output)
	if styled {
		return s.styledString()
	}
	return s.String()
}

type term struct {
	cols, rows  int
	grid        [][]rune
	faintGrid   [][]bool // cells written faint (SGR 2)
	faint       bool     // the pen
	x, y        int      // the cursor
	wrapPending bool     // the last column was written: the next character goes to the next row first
	budget      int
}

func newTerm(cols, rows int) *term {
	t := &term{cols: cols, rows: rows, budget: Budget}
	t.grid = make([][]rune, rows)
	t.faintGrid = make([][]bool, rows)
	for i := range t.grid {
		t.grid[i] = blankRow(cols)
		t.faintGrid[i] = make([]bool, cols)
	}
	return t
}

func blankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

// spend takes n from the budget and says whether any is left.
func (t *term) spend(n int) bool {
	t.budget -= n
	return t.budget > 0
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

const (
	stText = iota
	stEsc
	stCSI
	stString    // OSC, DCS, APC, PM, SOS: skipped up to BEL or ESC \
	stStringEsc // an ESC inside such a string
	stEscArg    // ESC ( and the like: one more byte, skipped
)

func (t *term) write(data []byte) {
	state := stText
	params := make([]int, 0, maxParams)
	cur, have, private, intermediate := 0, false, false, false
	for i := 0; i < len(data) && t.budget > 0; {
		b := data[i]
		switch state {
		case stText:
			switch {
			case b == 0x1b:
				state = stEsc
				i++
			case b < 0x20 || b == 0x7f:
				t.control(b)
				i++
			default:
				r, n := utf8.DecodeRune(data[i:])
				t.put(r)
				i += n
			}
		case stEsc:
			i++
			switch b {
			case '[':
				state, params, cur, have, private, intermediate = stCSI, params[:0], 0, false, false, false
			case ']', 'P', '_', '^', 'X':
				state = stString
			case '(', ')', '*', '+', '-', '.', '/', '#', '%', ' ':
				state = stEscArg
			case 0x1b:
				state = stEsc
			default:
				state = stText // any other ESC sequence is skipped
			}
		case stEscArg:
			i++
			state = stText
		case stString:
			i++
			if b == 0x07 {
				state = stText
			} else if b == 0x1b {
				state = stStringEsc
			}
		case stStringEsc:
			i++
			state = stString // an ESC not followed by \ is part of the string
			if b == '\\' {
				state = stText
			}
		case stCSI:
			i++
			switch {
			case b >= '0' && b <= '9':
				cur = min(cur*10+int(b-'0'), maxParam)
				have = true
			case b == ';' || b == ':':
				if len(params) < maxParams {
					params = append(params, cur)
				}
				cur, have = 0, false
			case b >= '<' && b <= '?':
				private = true
			case b >= 0x20 && b <= 0x2f:
				intermediate = true
			case b >= 0x40 && b <= 0x7e:
				if (have || len(params) > 0) && len(params) < maxParams {
					params = append(params, cur)
				}
				if !private && !intermediate {
					t.csi(b, params)
				}
				state = stText
			case b == 0x1b:
				state = stEsc // a sequence cut off by another
			}
		}
	}
}

func (t *term) control(b byte) {
	switch b {
	case '\r':
		t.x, t.wrapPending = 0, false
	case '\n', 0x0b, 0x0c:
		t.wrapPending = false
		t.lineFeed()
	case '\b':
		t.x, t.wrapPending = max(t.x-1, 0), false
	case '\t':
		t.x, t.wrapPending = min((t.x/8+1)*8, t.cols-1), false
	}
}

func (t *term) put(r rune) {
	if !t.spend(1) {
		return
	}
	if t.wrapPending {
		t.x, t.wrapPending = 0, false
		t.lineFeed()
	}
	if r == utf8.RuneError {
		r = ' '
	}
	t.grid[t.y][t.x] = r
	t.faintGrid[t.y][t.x] = t.faint
	if t.x == t.cols-1 {
		t.wrapPending = true
	} else {
		t.x++
	}
}

// lineFeed moves the cursor down a row, scrolling the whole screen up a row at its bottom.
func (t *term) lineFeed() {
	if t.y < t.rows-1 {
		t.y++
		return
	}
	if !t.spend(t.cols) {
		return
	}
	copy(t.grid, t.grid[1:])
	t.grid[t.rows-1] = blankRow(t.cols)
	copy(t.faintGrid, t.faintGrid[1:])
	t.faintGrid[t.rows-1] = make([]bool, t.cols)
}

func (t *term) clear(y, from, to int) {
	from, to = clamp(from, 0, t.cols), clamp(to, 0, t.cols)
	if to <= from || !t.spend(to-from) {
		return
	}
	row := t.grid[y]
	for i := from; i < to; i++ {
		row[i] = ' '
		t.faintGrid[y][i] = false
	}
}

// param is the i'th parameter, or def when it is missing or 0.
func param(params []int, i, def int) int {
	if i < len(params) && params[i] > 0 {
		return params[i]
	}
	return def
}

func (t *term) csi(final byte, params []int) {
	n := param(params, 0, 1)
	mode := 0
	if len(params) > 0 {
		mode = params[0]
	}
	if final == 'm' {
		t.sgr(params)
		return
	}
	t.wrapPending = false
	switch final {
	case 'A':
		t.y = max(t.y-n, 0)
	case 'B':
		t.y = min(t.y+n, t.rows-1)
	case 'C':
		t.x = min(t.x+n, t.cols-1)
	case 'D':
		t.x = max(t.x-n, 0)
	case 'E':
		t.x, t.y = 0, min(t.y+n, t.rows-1)
	case 'F':
		t.x, t.y = 0, max(t.y-n, 0)
	case 'G':
		t.x = clamp(n-1, 0, t.cols-1)
	case 'd':
		t.y = clamp(n-1, 0, t.rows-1)
	case 'H', 'f':
		t.y, t.x = clamp(param(params, 0, 1)-1, 0, t.rows-1), clamp(param(params, 1, 1)-1, 0, t.cols-1)
	case 'J':
		switch mode {
		case 0:
			t.clear(t.y, t.x, t.cols)
			for y := t.y + 1; y < t.rows; y++ {
				t.clear(y, 0, t.cols)
			}
		case 1:
			for y := 0; y < t.y; y++ {
				t.clear(y, 0, t.cols)
			}
			t.clear(t.y, 0, t.x+1)
		case 2, 3:
			for y := 0; y < t.rows; y++ {
				t.clear(y, 0, t.cols)
			}
		}
	case 'X':
		t.clear(t.y, t.x, t.x+n) // ECH: n cells from the cursor, the cursor stays (Codex erases the old selection marker this way)
	case 'K':
		switch mode {
		case 0:
			t.clear(t.y, t.x, t.cols)
		case 1:
			t.clear(t.y, 0, t.x+1)
		case 2:
			t.clear(t.y, 0, t.cols)
		}
	}
	// Anything else (styling, scroll regions, insert, delete, repeat, tab stops, reports) is skipped.
}

// sgr tracks only faint: 2 sets it, 0 and 22 clear it; the arguments of an extended colour (38/48/58 ;5;n or
// ;2;r;g;b) are skipped, so a colour number 2 is not read as faint.
func (t *term) sgr(params []int) {
	if len(params) == 0 {
		t.faint = false
		return
	}
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case 0, 22:
			t.faint = false
		case 2:
			t.faint = true
		case 38, 48, 58:
			if i+1 < len(params) && params[i+1] == 5 {
				i += 2
			} else if i+1 < len(params) && params[i+1] == 2 {
				i += 4
			}
		}
	}
}

// styledString is String with each run of faint cells wrapped in ESC[2m ... ESC[22m. A row's trailing blanks
// are trimmed as String's are.
func (t *term) styledString() string {
	lines := make([]string, t.rows)
	for y, row := range t.grid {
		runes := []rune(string(row))
		end := len(runes)
		for end > 0 && runes[end-1] == ' ' {
			end--
		}
		var b strings.Builder
		faint := false
		for x := 0; x < end; x++ {
			if f := t.faintGrid[y][x]; f != faint {
				if f {
					b.WriteString("\x1b[2m")
				} else {
					b.WriteString("\x1b[22m")
				}
				faint = f
			}
			b.WriteRune(runes[x])
		}
		if faint {
			b.WriteString("\x1b[22m")
		}
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

func (t *term) String() string {
	lines := make([]string, t.rows)
	for i, row := range t.grid {
		lines[i] = strings.TrimRight(string(row), " ")
	}
	return strings.Join(lines, "\n")
}
