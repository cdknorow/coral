package ptymanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

// Fuzz targets for the screen gate (#4147): what Coral concludes from an agent's terminal bytes,
// which are whatever the agent or a tool printed. Seeds are the recorded screens under
// testdata/screens (every agent CLI's captures) plus a few hostile shapes. A plain go test runs the
// seeds; go test -fuzz FuzzPromptOnScreen -fuzztime 10m ./internal/ptymanager/ fuzzes.

const screenBudget = 5 * time.Second // generous: a screen read is microseconds; this catches a quadratic or looping parse

func screenSeeds(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "screens", "*", "*", "*.ansi"))
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 {
			f.Add(string(b))
		}
	}
	for _, s := range []string{
		"", "\n", "\x1b[", "\x1b[999999999999999999999m", "\x1b]0;title\x07", "\r\r\r", "> ", "───\n> hi\n───",
		"╭──\n│ > draft │\n╰──", "1. a\n2. b\n3. c", "❯ 1. Yes\n  2. No", strings.Repeat("> ", 5000), strings.Repeat("─", 100000),
		strings.Repeat("line\n", 20000), "\xff\xfe\x00\x00", strings.Repeat("\x1b[1m", 20000) + "x",
	} {
		f.Add(s)
	}
}

func within(t *testing.T, what string, start time.Time) {
	t.Helper()
	if d := time.Since(start); d > screenBudget {
		t.Fatalf("%s took %s", what, d)
	}
}

// FuzzPromptOnScreen: never panics, finishes in bounded time, is deterministic, gives a reason
// whenever it says a prompt is open, and reads the same screen with its ANSI sequences already stripped.
func FuzzPromptOnScreen(f *testing.F) {
	screenSeeds(f)
	f.Fuzz(func(t *testing.T, screen string) {
		start := time.Now()
		open, why := PromptOnScreen(screen)
		within(t, "PromptOnScreen", start)
		if open && why == "" {
			t.Fatalf("a prompt is open with no reason")
		}
		if o2, w2 := PromptOnScreen(screen); o2 != open || w2 != why {
			t.Fatalf("not deterministic: %v %q then %v %q", open, why, o2, w2)
		}
		if o3, w3 := PromptOnScreen(screenText(screen)); o3 != open || w3 != why {
			t.Fatalf("the stripped screen reads differently: %v %q against %v %q", open, why, o3, w3)
		}
	})
}

// FuzzInputBox: the agent's input box read from the screen. It never panics, is bounded, answers
// nothing when a prompt is open (a menu replaces the box), and what it returns came from the screen.
func FuzzInputBox(f *testing.F) {
	screenSeeds(f)
	f.Fuzz(func(t *testing.T, screen string) {
		start := time.Now()
		text, ok := inputBoxText(screen)
		within(t, "inputBoxText", start)
		if open, _ := PromptOnScreen(screen); open && (ok || text != "") {
			t.Fatalf("a prompt is open yet the input box read %q %v", text, ok)
		}
		if !ok && text != "" {
			t.Fatalf("text %q with ok=false", text)
		}
		if ok && len(text) > len(screenText(screen))+len(strings.Split(screenText(screen), "\n")) {
			t.Fatalf("the box text (%d bytes) is longer than the screen", len(text))
		}
		for _, st := range []string{"", "hello", text, strings.Repeat("x", 300)} {
			s := time.Now()
			_ = inputBoxState(screen, st, false)
			_ = inputBoxState(screen, st, true)
			within(t, "inputBoxState", s)
		}
	})
}

// FuzzSqueeze: the white-space-free form two texts are compared by is idempotent and holds no
// space or box-drawing separators.
func FuzzSqueeze(f *testing.F) {
	for _, s := range []string{"", "a b", " a\tb\nc ", "│ a │", "a|b", "\u00a0\u2003x", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q := squeeze(s)
		if squeeze(q) != q {
			t.Fatalf("not idempotent: %q then %q", q, squeeze(q))
		}
		for _, r := range q {
			if unicode.IsSpace(r) || r == '│' || r == '┃' || r == '|' {
				t.Fatalf("%U left in %q", r, q)
			}
		}
		if len(q) > len(s)+3*len(s) { // invalid bytes become the 3-byte replacement rune
			t.Fatalf("grew from %d to %d bytes", len(s), len(q))
		}
	})
}
