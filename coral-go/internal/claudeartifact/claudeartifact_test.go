package claudeartifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinkIDs(t *testing.T) {
	id := "a1b2c3d4-e5f6"
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"plain link", "see https://claude.ai/artifact/" + id + " now", []string{id}},
		{"code path", "https://claude.ai/code/artifact/" + id, []string{id}},
		{"link ends at punctuation", "(https://claude.ai/artifact/" + id + ").", []string{id}},
		{"repeats once", "https://claude.ai/artifact/" + id + " https://claude.ai/artifact/" + id, []string{id}},
		{"two ids in order", "https://claude.ai/artifact/aaaaaaaa https://claude.ai/code/artifact/bbbbbbbb", []string{"aaaaaaaa", "bbbbbbbb"}},
		{"look-alike host with a suffix", "https://claude.ai.example.com/artifact/" + id, nil},
		{"look-alike host with a prefix", "https://notclaude.ai/artifact/" + id, nil},
		{"user info", "https://claude.ai@evil.example/artifact/" + id, nil},
		{"http is not it", "http://claude.ai/artifact/" + id, nil},
		{"not an artifact path", "https://claude.ai/chat/" + id, nil},
		{"too short", "https://claude.ai/artifact/abc", nil},
		{"too long", "https://claude.ai/artifact/" + strings.Repeat("a", 65), nil},
		{"no link", "nothing here", nil},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, LinkIDs(c.text), c.name)
	}
	var many strings.Builder
	for i := 0; i < 20; i++ {
		many.WriteString("https://claude.ai/artifact/id" + strings.Repeat(string(rune('a'+i)), 8) + " ")
	}
	assert.Len(t, LinkIDs(many.String()), MaxIDs, "a text gives at most MaxIDs")
}

func TestValidID(t *testing.T) {
	assert.True(t, ValidID("a1b2c3d4-e5f6"))
	assert.False(t, ValidID("short"))
	assert.False(t, ValidID(strings.Repeat("a", 65)))
	assert.False(t, ValidID("abcdefgh/../x"))
	assert.False(t, ValidID("abcdefgh\n"))
}

func TestLastAssistantIDsReadsTheNewestAssistantText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"old https://claude.ai/artifact/oldoldold1"}]}}`,
		`{"type":"user","message":{"role":"user","content":"https://claude.ai/artifact/useruser1234"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"},{"type":"text","text":"Published: https://claude.ai/artifact/newnewnew1"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"https://claude.ai/artifact/resultresult1"}]}}`,
	}
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	assert.Equal(t, []string{"newnewnew1"}, LastAssistantIDs(p), "the newest assistant text only: not the user's, not a tool result")
	assert.Nil(t, LastAssistantIDs(filepath.Join(dir, "missing.jsonl")))
}

func TestLastAssistantIDsReadsOnlyTheEndOfALargeFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.jsonl")
	head := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"https://claude.ai/artifact/farfarfar12"}]}}` + "\n"
	filler := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	require.NoError(t, os.WriteFile(p, []byte(head+strings.Repeat(filler, 100)), 0o600))
	assert.Nil(t, LastAssistantIDs(p), "an assistant line outside the last 64 KiB is not read")
}
