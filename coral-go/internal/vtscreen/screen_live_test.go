package vtscreen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A Screen fed in chunks of any size shows what Render shows for the whole output: a sequence or a character cut by a chunk's end is finished by the next.
func TestAScreenFedInChunksShowsWhatRenderShows(t *testing.T) {
	out := []byte("\x1b[2J\x1b[38;1H› Ask Codex to do anything\x1b[40;1H  ? for shortcuts\x1b[30;1H\x1b[2K• Working (8s • esc to interrupt)\x1b[1;32mgreen\x1b[0m é日本\r\nline two\r\n")
	want := Render(out, 120, 40)
	for _, n := range []int{1, 2, 3, 5, 7, 64, len(out)} {
		s := NewScreen(120, 40)
		for i := 0; i < len(out); i += n {
			s.Write(out[i:min(i+n, len(out))])
		}
		assert.Equal(t, want, s.Render(), "chunks of %d bytes", n)
	}
}

// What was drawn once stays, however much is written after it (the replay ring forgets it).
func TestAScreenKeepsWhatWasDrawnOnceThroughAHundredThousandRepaints(t *testing.T) {
	s := NewScreen(120, 40)
	s.Write([]byte("\x1b[2J\x1b[38;1H› Ask Codex to do anything"))
	for i := 0; i < 100000; i++ {
		s.Write([]byte("\x1b[30;1H\x1b[2K• Working (" + strings.Repeat("x", 40) + ")"))
	}
	got := s.Render()
	assert.Contains(t, got, "› Ask Codex to do anything")
	assert.True(t, s.Fed())
}

// The cost of hostile output is bounded per byte (Security's #6481 finding: short clear-screen sequences cost 1.4 to 1.9 s of CPU per MiB in the PTY read loop under a per-chunk budget). The cell
// operations a screen spends over any MiB are at most WorkPerByte x 1 MiB plus one burst (and one operation of overshoot per chunk), whatever the bytes are.
func TestTheWorkOfAMiBOfHostileOutputIsBoundedPerByte(t *testing.T) {
	const mib = 1 << 20
	// an operation is paid for when it starts, so each 32 KiB chunk may overshoot by the one that ran the credit out (a whole 120x40 clear at most)
	limit := int64(WorkPerByte*mib + WorkBurst + (mib/(32*1024)+1)*120*40)
	for name, unit := range map[string]string{
		"clear screen":         "\x1b[2J",
		"clear screen at home": "\x1b[H\x1b[2J",
		"line feeds":           "\n",
		"erase rows":           "\x1b[1;1H\x1b[2K",
		"erase lines below":    "\x1b[J",
	} {
		t.Run(name, func(t *testing.T) {
			s := NewScreen(120, 40)
			chunk := []byte(strings.Repeat(unit, 32*1024/len(unit)))
			for sent := 0; sent < mib; sent += len(chunk) {
				s.Write(chunk)
			}
			assert.LessOrEqual(t, s.Work(), limit, "a MiB of %q costs at most the rolling budget", unit)
		})
	}
}

// Ordinary output spends a small part of the budget and is unchanged: a screenful of text, scrolled output and repaints are all played as a plain Render plays them.
func TestOrdinaryOutputIsPlayedInFullWithinTheBudget(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString(fmt.Sprintf("line %d of ordinary output with some text on it\r\n", i))
		if i%100 == 0 {
			b.WriteString("\x1b[2J\x1b[1;1H") // a full repaint now and then, as a TUI does
		}
	}
	out := []byte(b.String())
	s := NewScreen(120, 40)
	for i := 0; i < len(out); i += 4096 {
		s.Write(out[i:min(i+4096, len(out))])
	}
	assert.Equal(t, Render(out, 120, 40), s.Render())
	assert.Less(t, s.Work(), int64(WorkPerByte*len(out))/4, "far inside the budget")
}

// The budget is rolling: output after a flood is played again as it earns credit.
func TestTheBudgetRefillsAfterAFlood(t *testing.T) {
	s := NewScreen(120, 40)
	flood := []byte(strings.Repeat("\x1b[2J", 32*1024/4))
	for i := 0; i < 64; i++ {
		s.Write(flood)
	}
	s.Write([]byte(strings.Repeat("quiet text ", 30000))) // earns credit
	s.Write([]byte("\x1b[2J\x1b[1;1Hback to normal"))
	assert.Contains(t, s.Render(), "back to normal")
}

func TestAResizedScreenKeepsWhatFits(t *testing.T) {
	s := NewScreen(20, 5)
	s.Write([]byte("\x1b[1;1Htop\x1b[5;1Hbottom"))
	s.Resize(30, 3)
	got := strings.Split(s.Render(), "\n")
	assert.Equal(t, "top", got[0])
	assert.Len(t, got, 3)
	s.Resize(10, 8)
	assert.Len(t, strings.Split(s.Render(), "\n"), 8)
	s.Write([]byte("\x1b[8;1Hlast"))
	assert.Contains(t, s.Render(), "last")
}
