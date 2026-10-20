package board

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// #5372: a board key becomes a URL path segment, a worktree folder and file names. Whatever the text, a key UsableBoardKey accepts must stay one
// plain path element: not empty, not . or .., no separator or drive character, no control or bidi character, and local to the folder it is joined to.
//
//	go test ./internal/board -run '^$' -fuzz FuzzUsableBoardKey -fuzztime 10m
func FuzzUsableBoardKey(f *testing.F) {
	for _, s := range []string{"", ".", "..", "team", "my-board_1.2", "a/b", `a\b`, "C:x", "a:b", "x\x00y", "ok\n", "‮txt", "⁦x", "‎", "é", strings.Repeat("a", 256), strings.Repeat("a", 257), "../x", "a/../b", "...", "-flag"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if !UsableBoardKey(key) {
			return
		}
		if key == "" || key == "." || key == ".." || len(key) > 256 {
			t.Fatalf("UsableBoardKey accepted %q", key)
		}
		if strings.ContainsAny(key, "/\\:\x00") {
			t.Fatalf("UsableBoardKey accepted a separator, a drive or a NUL in %q", key)
		}
		for _, r := range key {
			if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
				t.Fatalf("UsableBoardKey accepted the control or bidi character U+%04X in %q", r, key)
			}
		}
		if !filepath.IsLocal(key) {
			t.Fatalf("UsableBoardKey accepted %q, which is not local to the folder it is joined to", key)
		}
		if filepath.Base(key) != key || filepath.Clean(key) != key {
			t.Fatalf("UsableBoardKey accepted %q, which is not one plain path element", key)
		}
		if filepath.Dir(filepath.Join("base", key)) != "base" {
			t.Fatalf("UsableBoardKey accepted %q, which escapes or splits a folder when joined", key)
		}
	})
}
