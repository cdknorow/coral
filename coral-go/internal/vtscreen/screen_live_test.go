package vtscreen

import (
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

// Each chunk has its own work budget: a screen written a million times is not cut off at a total.
func TestEveryChunkHasTheWholeBudget(t *testing.T) {
	s := NewScreen(120, 40)
	for i := 0; i < 400; i++ {
		s.Write([]byte(strings.Repeat("\x1b[2J", 1500)))
	}
	s.Write([]byte("\x1b[1;1Hstill drawing"))
	assert.Contains(t, s.Render(), "still drawing")
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
