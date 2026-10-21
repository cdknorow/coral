package routes

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/redact"
)

// The memo answers what shownText answers, remembers it, never keeps a text the redactor could not check, and stays bounded.
func TestShownTextMemoizedIsShownText(t *testing.T) {
	resetShownMemo()
	t.Cleanup(resetShownMemo)
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	for _, s := range []string{"", "plain text", "key " + tok + " here", strings.Repeat("a", shownMemoMaxText+1), "日本語"} {
		assert.Equal(t, shownText(s), shownTextMemoized(s), "%.30q", s)
		assert.Equal(t, shownText(s), shownTextMemoized(s), "the second time, from the memo: %.30q", s)
	}
	assert.Contains(t, shownTextMemoized("key "+tok), redact.Marker)
	shownMemo.Lock()
	_, kept := shownMemo.m["plain text"]
	_, keptLong := shownMemo.m[strings.Repeat("a", shownMemoMaxText+1)]
	shownMemo.Unlock()
	assert.True(t, kept)
	assert.False(t, keptLong, "a long text is not kept")

	// a failing redactor hides the text and is not remembered
	resetShownMemo()
	orig := shownRedactor
	t.Cleanup(func() { shownRedactor = orig })
	calls := 0
	shownRedactor = func() redact.Redactor { calls++; return redact.Refuse }
	assert.Equal(t, "", shownTextMemoized("some text"))
	assert.Equal(t, "", shownTextMemoized("some text"))
	assert.Equal(t, 2, calls, "asked again: a failure is not kept")
	shownRedactor = orig
	assert.Equal(t, "some text", shownTextMemoized("some text"), "and the text is shown once the redactor works")
}

func TestShownMemoIsBoundedAndSafeForConcurrentUse(t *testing.T) {
	resetShownMemo()
	t.Cleanup(resetShownMemo)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				s := strings.Repeat("x", 1+(i*7+g)%900) + string(rune('a'+g))
				require.Equal(t, s, shownTextMemoized(s))
			}
		}(g)
	}
	wg.Wait()
	shownMemo.Lock()
	defer shownMemo.Unlock()
	assert.LessOrEqual(t, shownMemo.bytes, shownMemoLimit)
	// the whole memo is dropped when it fills
	shownMemo.m, shownMemo.bytes = map[string]string{"a": "a"}, shownMemoLimit
	shownMemo.Unlock()
	shownTextMemoized("fresh text")
	shownMemo.Lock()
	assert.Len(t, shownMemo.m, 1)
}
