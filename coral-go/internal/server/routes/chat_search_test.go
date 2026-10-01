package routes

import (
	"strings"
	"testing"
)

func TestSearchExcerptAdjustsOffsetsAndBoundsOutput(t *testing.T) {
	text := strings.Repeat("prefix ", 80) + "needle" + strings.Repeat(" suffix", 80)
	offset := strings.Index(text, "needle")
	excerpt, offsets := searchExcerpt(text, [][]int{{offset, offset + 6}}, 40)
	if len(excerpt) > 40 {
		t.Fatalf("excerpt length=%d, want <=40", len(excerpt))
	}
	if len(offsets) != 1 || offsets[0][1]-offsets[0][0] != 6 {
		t.Fatalf("offsets=%v, want one six-character match", offsets)
	}
	if excerpt[offsets[0][0]:offsets[0][1]] != "needle" {
		t.Fatalf("highlight=%q, want needle", excerpt[offsets[0][0]:offsets[0][1]])
	}
}

func TestSearchExcerptPreservesMultipleMatches(t *testing.T) {
	text := "one needle and another needle"
	excerpt, offsets := searchExcerpt(text, [][]int{{4, 10}, {21, 27}}, 200)
	if excerpt != text || len(offsets) != 2 {
		t.Fatalf("excerpt=%q offsets=%v", excerpt, offsets)
	}
}
