package jsonl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptCursorHeldLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	var c TranscriptCursor
	var got []string
	scan := func() {
		c.Scan(path, func() { got = append(got, "<reset>") }, func(b []byte) { got = append(got, string(b)) })
	}
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appendS := func(s string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	expect := func(want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %q, want %q", got, want)
			}
		}
		got = nil
	}

	// A complete JSON last line without a newline: held on the scan that
	// reads it, delivered on the next scan that finds the file unchanged.
	write("{\"a\":1}\n{\"b\":2}")
	scan()
	expect("<reset>", "{\"a\":1}\n")
	if !c.Pending() {
		t.Fatal("the unterminated line should be pending")
	}
	scan()
	expect("{\"b\":2}")
	scan()
	expect()
	// Its newline arriving later delivers nothing twice.
	appendS("\n{\"c\":3}\n")
	scan()
	expect("\n", "{\"c\":3}\n")

	// An incomplete line (mid-write) stays held while the file is unchanged,
	// and is delivered exactly once when it is finished.
	appendS("{\"d\":")
	scan()
	expect()
	scan()
	expect()
	if !c.Pending() {
		t.Fatal("an incomplete line stays pending")
	}
	appendS("4}\n")
	scan()
	expect("{\"d\":4}\n")
	if c.Pending() {
		t.Fatal("nothing pending after the newline")
	}
}
