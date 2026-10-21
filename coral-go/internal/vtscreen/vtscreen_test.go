package vtscreen

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex draws a menu as ratatui does (#4543): every row, and every word of a row, placed with a cursor move,
// no newline anywhere, inside a synchronized update; a later frame redraws only the rows that changed.
const codexStyleMenu = "\x1b[?2026h\x1b[1;1H\x1b[J" +
	"\x1b[2;3H\x1b[1mHooks need review" +
	"\x1b[4;1H\x1b[22m\x1b[38;5;6;49m› 1. Review hooks" +
	"\x1b[5;3H\x1b[39;49m2.\x1b[5;6HTrust\x1b[5;12Hall\x1b[5;16Hand\x1b[5;20Hcontinue" +
	"\x1b[7;3H\x1b[2mPress enter to confirm or esc to go back\x1b[0m\x1b[?2026l" +
	"\x1b[?2026h\x1b[4;1H  1. Review hooks\x1b[5;1H\x1b[1m› 2. Trust all and continue\x1b[0m\x1b[?2026l"

func TestRenderDrawsCursorPlacedRows(t *testing.T) {
	got := strings.Split(Render([]byte(codexStyleMenu), 60, 8), "\n")
	require.Len(t, got, 8, "one line per row")
	assert.Equal(t, "  Hooks need review", got[1])
	assert.Equal(t, "  1. Review hooks", got[3], "the later frame's redraw replaced the row")
	assert.Equal(t, "› 2. Trust all and continue", got[4], "words placed one by one read as one line")
	assert.Equal(t, "  Press enter to confirm or esc to go back", got[6])
	assert.Equal(t, "", got[7], "trailing blanks trimmed")
	// In the raw text of the first frame, what the prompt reader got before, the label is not there.
	first := codexStyleMenu[:strings.Index(codexStyleMenu, "\x1b[?2026l")]
	assert.Contains(t, Render([]byte(first), 60, 8), "2. Trust all and continue")
	assert.NotContains(t, first, "Trust all and continue")
}

// What Claude Code sends at launch around its first screen: a kitty keyboard query (a general emulator took
// it for "restore the cursor"), a graphics query whose payload must not print, a mode report request. None
// draws.
func TestRenderIgnoresTerminalQueries(t *testing.T) {
	out := "banner line 1\r\nbanner line 2\r\n" +
		"\x1b[>0q\x1b[?u\x1b]7501;?\x07\x1b[c\x1b[?2026$p\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[16t\x1b[0 q" +
		"\x1b[?25l\x1b[?2004hDo you want to use this API key?"
	got := strings.Split(Render([]byte(out), 60, 4), "\n")
	assert.Equal(t, []string{"banner line 1", "banner line 2", "Do you want to use this API key?", ""}, got)
}

func TestRenderWithoutASize(t *testing.T) {
	assert.Len(t, strings.Split(Render([]byte("hi"), 0, 0), "\n"), DefaultRows)
}

// An agent controls every byte the renderer reads, and the ring is replayed on every read (Security on
// #4543: a general emulator spun minutes on "ESC [ 2147483647 I" and panicked on negative parameters). Each of
// these renders in well under 100 ms, without a panic.
func TestRenderHostileSequences(t *testing.T) {
	cases := []string{
		"\x1b[2147483647I", "\x1b[99999999999Z", "\x1b[2147483647Z", "\x1b[2147483647b",
		"\x1b[?-1h\x1b[-1;0P\x1b[-1;0@", "\x1b[;0f\x1b[-1;0@\x1b[-1;-1H", "\x1b[-2147483648L\x1b[-5M",
		"\x1b[0;0r\x1b[99999;1r\x1b[2147483647S\x1b[2147483647T", "\x1b[" + strings.Repeat("9;", 5000) + "H",
		"\x1b[" + strings.Repeat("1", 100000) + "C", "\x1b]" + strings.Repeat("x", 100000), "\x1b[?1049h\x1b[?1049l\x1b[?1049h",
		"\x1bc\x1b7\x1b[2147483647;2147483647H\x1b8", "\x1b[\x1b[\x1b[", "\x1b[1;2;3;4;5;6;7;8;9;10;11;12;13;14;15;16;17;18;19;20m",
	}
	for _, f := range "@ABCDEFGHIJKLMPSTXZ`abdefghlmnrstu" {
		cases = append(cases, fmt.Sprintf("\x1b[2147483647%c", f), fmt.Sprintf("\x1b[2147483647;2147483647%c", f), fmt.Sprintf("\x1b[-1%c", f))
	}
	before := recovered.Load()
	defer func() {
		assert.Equal(t, before, recovered.Load(), "no render panicked (a recovered panic is a missing bound)")
	}()
	for _, seq := range cases {
		start := time.Now()
		var got string
		require.NotPanics(t, func() { got = Render([]byte("abc\r\n"+seq+"xyz"), 164, 44) }, "%q", seq)
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("%q took %v", seq, d)
		}
		assert.Len(t, strings.Split(got, "\n"), 44, "%q", seq)
	}
}

// An input made only of the most expensive sequences stops at the work budget: a whole screen cleared, or
// scrolled, for every few bytes of MaxInput is cut off once Budget cell operations are spent.
// Render caps its input and counts every cell it writes, clears or scrolls against Budget, so the work of
// any input is bounded by that count whatever the sequence; a regression to super-linear work (a scroll
// that shifts the whole grid once per newline without paying for it) shows as work that is not counted
// against the budget, and a regression that counts too little shows as an input that is never cut off.
// The test reads the budget a render consumed instead of timing it, so it does not depend on CPU speed or
// on the gate's load (#4958; it was a wall-clock ratio against a linear baseline, #4909).
func TestRenderWorkIsBounded(t *testing.T) {
	// work plays input through a full-size screen and returns the budget it consumed.
	work := func(input []byte) int {
		if len(input) > MaxInput {
			input = input[len(input)-MaxInput:]
		}
		s := newTerm(MaxCols, MaxRows)
		s.write(input)
		return Budget - s.budget
	}

	// The linear baseline: each printable byte costs one cell operation, and each full row it wraps past the
	// bottom scrolls once more for a row's cells, so plain text costs under twice its length.
	plain := work([]byte(strings.Repeat("a", MaxInput)))
	assert.GreaterOrEqual(t, plain, MaxInput, "printable text costs an operation a byte")
	assert.Less(t, plain, 2*MaxInput, "plain text costs a constant a byte, scrolling included")

	// No sequence may spend more than the budget plus the sequence that crosses it (at most a whole screen).
	const overshoot = MaxCols * MaxRows
	for _, seq := range []string{"\x1b[2J", "\x1b[J", "\n", "\x1b[200S", "\x1b[?1049h\x1b[?1049l", "\x1bc"} {
		input := []byte(strings.Repeat(seq, MaxInput/len(seq)+1))
		got := work(input)
		t.Logf("%q: %d of %d", seq, got, Budget)
		assert.LessOrEqual(t, got, Budget+overshoot, "an input of %q spent more than the budget (work not counted or not bounded)", seq)
	}

	// The two that redraw or scroll the whole screen are stopped BY the budget: a full input would cost far more.
	for _, seq := range []string{"\x1b[2J", "\n"} {
		input := []byte(strings.Repeat(seq, MaxInput/len(seq)+1))
		assert.GreaterOrEqual(t, work(input), Budget, "an input of %q is cut off by the budget, not run to its end", seq)
	}
}

// A parameter is capped as it is read: 2^64 + 3 would wrap around to 3 (column 3); capped, it is the last
// column.
func TestParametersAreCappedAsRead(t *testing.T) {
	assert.Equal(t, strings.Repeat(" ", 9)+"x", strings.Split(Render([]byte("\x1b[18446744073709551619Gx"), 10, 2), "\n")[0])
}

// A sequence with a private marker asks or sets something and is skipped, whatever its final byte: "ESC [ ? 2 J"
// (a selective erase) is not a clear screen.
func TestPrivateSequencesAreSkipped(t *testing.T) {
	assert.Equal(t, "abcd", strings.Split(Render([]byte("ab\x1b[?2Jcd\x1b[>1K\x1b[?5H"), 10, 2), "\n")[0])
}

// Only the end of a long output is played: what came before the last MaxInput bytes is not drawn.
func TestOnlyTheEndOfTheOutputIsPlayed(t *testing.T) {
	out := Render([]byte("EARLY"+strings.Repeat("\x1b[0m", MaxInput/4+8)+"late"), 20, 3)
	assert.NotContains(t, out, "EARLY")
	assert.Contains(t, out, "late")
}

// Random escape-laden output never panics and never takes long (go test -fuzz=FuzzRender; the seed corpus
// runs in every go test).
func FuzzRender(f *testing.F) {
	f.Add([]byte(codexStyleMenu), 80, 24)
	f.Add([]byte("\x1b[2147483647I\x1b[-1;0P\x1b[?1049h\x1b]0;t\x07\x1b[5;5r\x1bM\x1b[3L"), 164, 44)
	f.Add([]byte("\x1b[;0f\x1b[-1;0@\r\n\t\x08\x1b7\x1b8\x1b[s\x1b[u"), 1, 1)
	f.Fuzz(func(t *testing.T, data []byte, cols, rows int) {
		done := make(chan struct{})
		before := recovered.Load()
		go func() {
			defer close(done)
			_ = Render(data, cols%600, rows%300)
		}()
		select {
		case <-done:
			if recovered.Load() != before {
				t.Fatalf("rendering %q at %dx%d panicked", data, cols, rows)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("rendering %d bytes at %dx%d took over 2 s", len(data), cols, rows)
		}
	})
}

// ECH (CSI n X) erases n cells from the cursor and leaves the cursor: Codex redraws a menu by erasing the old
// selection marker this way, and without it the old marker stayed on screen beside the new one.
func TestEraseCharactersClearsFromTheCursor(t *testing.T) {
	got := Render([]byte("\x1b[1;1H› 1. Review hooks\x1b[1;1H\x1b[2X"), 40, 1)
	assert.Equal(t, "  1. Review hooks", got)
	// Without a count it erases one cell; past the line's end it stops at the edge.
	assert.Equal(t, "ab def", Render([]byte("abcdef\x1b[1;3H\x1b[X"), 10, 1))
	assert.Equal(t, "a", Render([]byte("abc\x1b[1;2H\x1b[500X"), 5, 1))
}

// A full-width line leaves the cursor pending past the last column; styling between that line and the next must not
// cancel the pending wrap (Codex draws `  1. Review hooks<padding>` then `ESC[1m ESC[7m› 2. ...`, and the "›" landed on the
// end of line 1).
func TestStylingDoesNotCancelAPendingWrap(t *testing.T) {
	got := Render([]byte("\x1b[1;1H"+strings.Repeat("a", 10)+"\x1b[1m\x1b[7mb"), 10, 3)
	assert.Equal(t, "aaaaaaaaaa\nb\n", got)
}
