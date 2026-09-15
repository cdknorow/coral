package gitutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewFileLineCount(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A trailing newline terminates the last line; it does not start another.
	if got := NewFileLineCount(write("three.txt", "a\nb\nc\n")); got != 3 {
		t.Errorf("three.txt = %d, want 3", got)
	}
	// An unterminated final line still counts.
	if got := NewFileLineCount(write("unterminated.txt", "a\nb\nc")); got != 3 {
		t.Errorf("unterminated.txt = %d, want 3", got)
	}
	if got := NewFileLineCount(write("one.txt", "only")); got != 1 {
		t.Errorf("one.txt = %d, want 1", got)
	}
	if got := NewFileLineCount(write("empty.txt", "")); got != 0 {
		t.Errorf("empty.txt = %d, want 0", got)
	}
	if got := NewFileLineCount(filepath.Join(dir, "missing.txt")); got != 0 {
		t.Errorf("missing file = %d, want 0", got)
	}
	if got := NewFileLineCount(dir); got != 0 {
		t.Errorf("directory = %d, want 0", got)
	}
	big := write("big.txt", strings.Repeat("x\n", MaxNewFileStatBytes))
	if got := NewFileLineCount(big); got != 0 {
		t.Errorf("oversized file = %d, want 0", got)
	}

	// Binary blobs (a SQLite WAL, say) have newline bytes but no line count.
	bin := write("data.bin", "\x00\x01\n\n\nbinary\n")
	if got := NewFileLineCount(bin); got != 0 {
		t.Errorf("binary file = %d, want 0", got)
	}
	// A NUL past the sniff window is not inspected, same as git.
	late := write("late.bin", strings.Repeat("a\n", binarySniffBytes)+"\x00")
	if got := NewFileLineCount(late); got != binarySniffBytes+1 {
		t.Errorf("late-NUL file = %d, want %d", got, binarySniffBytes+1)
	}
}
