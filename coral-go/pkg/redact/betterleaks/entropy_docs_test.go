package betterleaks

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent_docs/redaction.md lists every embedded rule with an entropy threshold (#5132: coral-hub reads it). A library update that adds, removes or
// changes one fails here until the table is updated.
func TestEntropyTableInTheDocsIsTheLibrarys(t *testing.T) {
	doc, err := os.ReadFile("../../../agent_docs/redaction.md")
	require.NoError(t, err)
	text := string(doc)
	start := strings.Index(text, "<!-- entropy-table:start -->")
	end := strings.Index(text, "<!-- entropy-table:end -->")
	require.True(t, start >= 0 && end > start, "the table's markers")
	have := map[string]string{}
	for _, line := range strings.Split(text[start:end], "\n") {
		cells := strings.Split(strings.Trim(line, "| "), "|")
		if len(cells) == 2 && strings.HasPrefix(strings.TrimSpace(cells[0]), "`") {
			have[strings.Trim(strings.TrimSpace(cells[0]), "`")] = strings.TrimSpace(cells[1])
		}
	}
	rules, err := EntropyGatedRules()
	require.NoError(t, err)
	want := map[string]string{}
	for _, r := range rules {
		assert.False(t, r.TokenEfficiency, "%s: the doc says no embedded rule uses the token-efficiency gate", r.ID)
		want[r.ID] = fmt.Sprintf("%.2f", r.Entropy)
	}
	assert.Equal(t, want, have)
	assert.Greater(t, len(want), 100, "the embedded rules have their thresholds")
}
