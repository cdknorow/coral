package hubharness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secrets"
)

// samples per shape, as coral-hub's harness measures (N=2000).
const samples = 2000

// How often does the redactor Coral Hub sees leave the random part of a secret in a board reply? One message per shape, replied as the board's
// JSON, as the hub's view-leak test posts them. Every shape must have zero misses, the ones coral-hub lists as known gaps of the library's rules
// included: the keyword rules (#5141) close them.
func TestHubRedactorMissRate(t *testing.T) {
	t.Setenv(secrets.KeywordsEnv, "")
	require.NoError(t, secrets.Init())
	rd := secrets.HubRedactor(func() bool { return true })
	miss := map[string]int{}
	first := map[string]string{}
	var names []string
	for i := 0; i < samples; i++ {
		shapes := leakShapes(&T{})
		var msgs []map[string]string
		for _, s := range shapes {
			msgs = append(msgs, map[string]string{"content": s.text})
		}
		raw, err := json.Marshal(map[string]any{"messages": msgs})
		require.NoError(t, err)
		out, err := rd.Reply(raw, true)
		require.NoError(t, err)
		body := string(out)
		for _, s := range shapes {
			if i == 0 {
				names = append(names, s.name)
			}
			if strings.Contains(body, s.secret) {
				miss[s.name]++
				if first[s.name] == "" {
					first[s.name] = s.text
				}
			}
		}
	}
	require.NotEmpty(t, names)
	for _, n := range names {
		assert.Zero(t, miss[n], "%s: %d of %d missed; the first: %.80q", n, miss[n], samples, first[n]) // assert, not require: one run lists every shape that leaks
	}
}

// With the keyword rules off the redactor is today's: the harness finds the gaps again (so the test above measures the rules, not the library).
func TestHubRedactorWithKeywordsOffHasTheKnownGaps(t *testing.T) {
	require.NoError(t, secrets.Init())
	rd := secrets.HubRedactor(func() bool { return false })
	missed := 0
	for i := 0; i < 50; i++ {
		for _, s := range leakShapes(&T{}) {
			raw, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"content": s.text}}})
			out, err := rd.Reply(raw, true)
			require.NoError(t, err)
			if strings.Contains(string(out), s.secret) {
				missed++
			}
		}
	}
	require.NotZero(t, missed, "the shapes coral-hub lists as known gaps leak without the keyword rules")
}
