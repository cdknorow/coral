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
	"sync"
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
	// The most scrolled-off rows RenderScrollback keeps (each is one row of at most MaxCols cells).
	MaxHistory = 1000
	// Parameters past this many are dropped, and each is capped while it is read.
	maxParams, maxParam = 16, 9999
)

// recovered counts the renders that panicked: a bug, and the tests require it to stay 0.
var recovered atomic.Int64

// RenderStyled is Render with each faint cell (SGR 2) marked in the text as the SGR sequences tmux's
// capture-pane -e prints (ESC[2m ... ESC[22m... per row), so a reader of the styled screen can tell Claude's
// faint prompt suggestion from a person's text (F10). No other styling is kept.
func RenderStyled(output []byte, cols, rows int) (screen string) {
	return render(output, cols, rows, true, 0)
}

// Render plays output through a cols x rows screen and returns its rows, each with trailing blanks trimmed.
func Render(output []byte, cols, rows int) (screen string) {
	return render(output, cols, rows, false, 0)
}

// RenderScrollback is Render with up to history of the rows that scrolled off the top kept above the
// screen, as tmux's capture-pane -p -S -<history> gives them: the scrolled-off rows (oldest first), then
// the screen's rows. history is capped at MaxHistory. Unlike tmux's scroll-on-clear, a screen cleared with
// ED is not pushed into the history: a TUI that clears and redraws every frame would fill it with old frames.
func RenderScrollback(output []byte, cols, rows, history int) string {
	return render(output, cols, rows, false, clamp(history, 0, MaxHistory))
}

func render(output []byte, cols, rows int, styled bool, history int) (screen string) {
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
	s.keep = history
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
	alt         *altSave // the main screen, while the alternate screen is shown (nil otherwise)
	x, y        int      // the cursor
	wrapPending bool     // the last column was written: the next character goes to the next row first
	budget      int
	keep        int      // how many scrolled-off rows to keep (RenderScrollback), 0 for none
	history     [][]rune // the rows scrolled off the top, oldest first (String shows the last keep)
	ps          parser   // where the escape-sequence parser is between writes (a Screen is fed chunk by chunk)
}

// parser is the escape-sequence parser's state, kept on the term so a sequence cut by the end of one chunk is finished by the next.
type parser struct {
	state                       int
	params                      []int
	cur                         int
	have, private, intermediate bool
	privChar                    byte
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
	if t.ps.params == nil {
		t.ps.params = make([]int, 0, maxParams)
	}
	state, params := t.ps.state, t.ps.params
	cur, have, private, intermediate, privChar := t.ps.cur, t.ps.have, t.ps.private, t.ps.intermediate, t.ps.privChar
	defer func() {
		t.ps = parser{state: state, params: params, cur: cur, have: have, private: private, intermediate: intermediate, privChar: privChar}
	}()
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
				private, privChar = true, b
			case b >= 0x20 && b <= 0x2f:
				intermediate = true
			case b >= 0x40 && b <= 0x7e:
				if (have || len(params) > 0) && len(params) < maxParams {
					params = append(params, cur)
				}
				if !private && !intermediate {
					t.csi(b, params)
				} else if private && privChar == '?' && !intermediate && (b == 'h' || b == 'l') {
					t.privateMode(params, b == 'h')
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
	w := runeWidth(r)
	if w == 0 {
		return // a combining mark or a zero-width character: it draws no cell of its own
	}
	if w == 2 && t.cols < 2 {
		w = 1 // a one-column screen has no room for the right half
	}
	if w == 2 && t.x == t.cols-1 {
		// A double-width character that does not fit on this row goes to the next.
		t.grid[t.y][t.x] = ' '
		t.x = 0
		t.lineFeed()
	}
	t.breakWide(t.y, t.x)
	if w == 2 {
		t.breakWide(t.y, t.x+1)
	}
	t.grid[t.y][t.x] = r
	t.faintGrid[t.y][t.x] = t.faint
	if w == 2 {
		t.grid[t.y][t.x+1] = 0 // the right half of the character: draws nothing
		t.faintGrid[t.y][t.x+1] = t.faint
	}
	if t.x+w >= t.cols {
		t.x = t.cols - 1
		t.wrapPending = true
	} else {
		t.x += w
	}
}

// breakWide blanks the other half of a double-width character that a write at (y, x) is about to cut in two.
func (t *term) breakWide(y, x int) {
	if x >= t.cols {
		return
	}
	if t.grid[y][x] == 0 && x > 0 {
		t.grid[y][x-1] = ' ' // x is the right half: the left half goes
	} else if x+1 < t.cols && t.grid[y][x+1] == 0 {
		t.grid[y][x+1] = ' ' // x is a left half: the right half goes
	}
}

// runeWidth is the number of cells r takes: 2 for East Asian wide and fullwidth characters and emoji, 0 for combining and
// zero-width ones, else 1 (as wcwidth, which tmux and the other emulators use).
func runeWidth(r rune) int {
	switch {
	case r >= 0x0300 && r <= 0x036f, r >= 0x200b && r <= 0x200f, r >= 0xfe00 && r <= 0xfe0f, r == 0x2060, r == 0xfeff,
		r >= 0x1ab0 && r <= 0x1aff, r >= 0x20d0 && r <= 0x20ff, r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x231a && r <= 0x231b, r >= 0x2329 && r <= 0x232a, r >= 0x23e9 && r <= 0x23ec,
		r == 0x23f0, r == 0x23f3, r >= 0x25fd && r <= 0x25fe, r >= 0x2614 && r <= 0x2615, r >= 0x2648 && r <= 0x2653,
		r == 0x267f, r == 0x2693, r == 0x26a1, r >= 0x26aa && r <= 0x26ab, r >= 0x26bd && r <= 0x26be, r >= 0x26c4 && r <= 0x26c5,
		r == 0x26ce, r == 0x26d4, r == 0x26ea, r >= 0x26f2 && r <= 0x26f3, r == 0x26f5, r == 0x26fa, r == 0x26fd, r == 0x2705,
		r >= 0x270a && r <= 0x270b, r == 0x2728, r == 0x274c, r == 0x274e, r >= 0x2753 && r <= 0x2755, r == 0x2757,
		r >= 0x2795 && r <= 0x2797, r == 0x27b0, r == 0x27bf, r >= 0x2b1b && r <= 0x2b1c, r == 0x2b50, r == 0x2b55,
		r >= 0x2e80 && r <= 0x303e, r >= 0x3041 && r <= 0x33ff, r >= 0x3400 && r <= 0x4dbf, r >= 0x4e00 && r <= 0xa4cf,
		r >= 0xa960 && r <= 0xa97f, r >= 0xac00 && r <= 0xd7a3, r >= 0xf900 && r <= 0xfaff, r >= 0xfe10 && r <= 0xfe19,
		r >= 0xfe30 && r <= 0xfe6f, r >= 0xff00 && r <= 0xff60, r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f680 && r <= 0x1f6ff, r >= 0x1f900 && r <= 0x1f9ff, r >= 0x1fa70 && r <= 0x1faff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// rowText is a row's text: the right halves of double-width characters draw nothing, and trailing blanks go.
func rowText(row []rune) string {
	var b strings.Builder
	for _, r := range row {
		if r != 0 {
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), " ")
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
	if t.keep > 0 && t.alt == nil { // the alternate screen has no scrollback, as in tmux
		// The row leaving the top is kept (its own slice: a scrolled-in row is always a new blankRow).
		// Trimmed back to keep only when twice that, so a scroll costs O(1) however long the output.
		t.history = append(t.history, t.grid[0])
		if len(t.history) >= 2*t.keep {
			t.history = append(t.history[:0:0], t.history[len(t.history)-t.keep:]...)
		}
	}
	// The row leaving the top is blanked and put back at the bottom, not allocated anew, unless it was kept as history (a flood of line feeds then costs no allocation).
	top, topFaint := t.grid[0], t.faintGrid[0]
	copy(t.grid, t.grid[1:])
	copy(t.faintGrid, t.faintGrid[1:])
	if t.keep > 0 && t.alt == nil {
		top, topFaint = blankRow(t.cols), make([]bool, t.cols)
	} else {
		for i := range top {
			top[i] = ' '
		}
		clear(topFaint)
	}
	t.grid[t.rows-1], t.faintGrid[t.rows-1] = top, topFaint
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
		runes := row
		end := len(runes)
		for end > 0 && (runes[end-1] == ' ' || runes[end-1] == 0) {
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
			if runes[x] != 0 {
				b.WriteRune(runes[x])
			}
		}
		if faint {
			b.WriteString("\x1b[22m")
		}
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

func (t *term) String() string {
	history := t.history
	if len(history) > t.keep {
		history = history[len(history)-t.keep:]
	}
	lines := make([]string, 0, len(history)+t.rows)
	for _, row := range history {
		lines = append(lines, rowText(row))
	}
	for _, row := range t.grid {
		lines = append(lines, rowText(row))
	}
	return strings.Join(lines, "\n")
}

// altSave is the main screen kept while an application is on the alternate screen (DECSET 47, 1047, 1049).
type altSave struct {
	grid      [][]rune
	faintGrid [][]bool
	x, y      int
}

// privateMode handles DECSET and DECRST: only the alternate screen is interpreted (47, 1047, 1049). Codex and agy switch to
// it; the main screen they leave comes back when they exit, as in a real terminal. Every other mode is skipped.
func (t *term) privateMode(params []int, set bool) {
	for _, p := range params {
		if p != 47 && p != 1047 && p != 1049 {
			continue
		}
		if set && t.alt == nil {
			if !t.spend(t.rows * t.cols) {
				return
			}
			t.alt = &altSave{grid: t.grid, faintGrid: t.faintGrid, x: t.x, y: t.y}
			t.grid = make([][]rune, t.rows)
			t.faintGrid = make([][]bool, t.rows)
			for i := range t.grid {
				t.grid[i] = blankRow(t.cols)
				t.faintGrid[i] = make([]bool, t.cols)
			}
			t.wrapPending = false
		} else if !set && t.alt != nil {
			t.grid, t.faintGrid, t.x, t.y = t.alt.grid, t.alt.faintGrid, t.alt.x, t.alt.y
			t.alt, t.wrapPending = nil, false
		}
	}
}

// Screen is a terminal screen kept as output arrives, for a reader that must not lose what an application drew once and never drew again (#6481): a TUI that repaints only the cells that change
// (Codex's composer is drawn at the start and again only when it changes) leaves nothing of it in the tail of a replay ring after a few minutes of work, and a screen played from that tail has no
// input box. The same deliberately small interpreter as Render, fed chunk by chunk: a sequence or a character cut by a chunk's end is finished by the next.
//
// The work is a rolling budget, not one per chunk (Security's #6481 finding: a hostile stream of short clear-screen sequences cost 1.4 to 1.9 s of CPU per MiB inside the PTY read loop): every byte
// written earns WorkPerByte cell operations, a burst of at most WorkBurst is kept, and an operation the credit cannot pay for is skipped, so the cost of any MiB of output is bounded
// (WorkPerByte x 1 MiB plus one burst, and one operation of overshoot per chunk) whatever it holds, while ordinary output (a cell per byte, a scrolled row per line feed) stays far inside it.
type Screen struct {
	mu     sync.Mutex
	t      *term
	carry  []byte // the start of a UTF-8 character cut by the end of a chunk
	fed    bool
	credit int   // cell operations the next bytes may spend (at most WorkBurst)
	work   int64 // cell operations spent in all (a test pins the bound)
}

// WorkPerByte and WorkBurst are the rolling budget of a live Screen: 128 cell operations earned per byte written (a line feed on a 120-column screen costs 120, a clear of one row up to
// 500), and a burst of 4M kept for one chunk of 32 KiB at that rate.
const (
	WorkPerByte = 128
	WorkBurst   = 4 << 20
)

// NewScreen is a blank cols x rows screen (the sizes Render takes).
func NewScreen(cols, rows int) *Screen {
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	return &Screen{t: newTerm(min(cols, MaxCols), min(rows, MaxRows)), credit: WorkBurst}
}

// Fed reports whether any output has been written to the screen.
func (s *Screen) Fed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fed
}

// Write plays a chunk of output. A panic (a bug) is recovered and the screen kept as it was.
func (s *Screen) Write(data []byte) {
	if s == nil || len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			recovered.Add(1)
			slog.Warn("vtscreen: recovered a panic while writing a live screen", "panic", r)
		}
	}()
	if len(s.carry) > 0 {
		data = append(append([]byte(nil), s.carry...), data...)
		s.carry = nil
	}
	// A character cut at the end waits for its rest (only inside text: an escape sequence's bytes are ASCII).
	for back := 1; back <= utf8.UTFMax-1 && back <= len(data); back++ {
		b := data[len(data)-back]
		if b < 0x80 {
			break
		}
		if utf8.RuneStart(b) {
			if !utf8.FullRune(data[len(data)-back:]) {
				s.carry = append([]byte(nil), data[len(data)-back:]...)
				data = data[:len(data)-back]
			}
			break
		}
	}
	s.fed = true
	s.credit = min(WorkBurst, s.credit+len(data)*WorkPerByte)
	s.t.budget = min(Budget, s.credit)
	start := s.t.budget
	s.t.write(data)
	spent := start - s.t.budget
	s.credit = max(0, s.credit-spent)
	s.work += int64(spent)
}

// Work is the cell operations the screen has spent in all.
func (s *Screen) Work() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.work
}

// Resize changes the screen's size, keeping what fits (an application repaints on its resize signal). The cursor is kept on the screen.
func (s *Screen) Resize(cols, rows int) {
	if s == nil || cols <= 0 || rows <= 0 {
		return
	}
	cols, rows = min(cols, MaxCols), min(rows, MaxRows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols == s.t.cols && rows == s.t.rows {
		return
	}
	n := newTerm(cols, rows)
	n.ps = s.t.ps
	n.faint = s.t.faint
	for y := 0; y < min(rows, s.t.rows); y++ {
		copy(n.grid[y], s.t.grid[y][:min(cols, s.t.cols)])
		copy(n.faintGrid[y], s.t.faintGrid[y][:min(cols, s.t.cols)])
	}
	n.x, n.y = min(s.t.x, cols-1), min(s.t.y, rows-1)
	s.t = n
}

// Render is the screen's rows as text, each with trailing blanks trimmed.
func (s *Screen) Render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t.String()
}

// RenderStyled is Render with each faint cell marked, as the package's RenderStyled.
func (s *Screen) RenderStyled() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t.styledString()
}
