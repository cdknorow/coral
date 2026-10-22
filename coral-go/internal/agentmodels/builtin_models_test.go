package agentmodels

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/proxy"
)

// The list a picker shows when the CLI does not answer (#5044): the current Claude models, by name,
// the preferred one first, with the levels the server passes the CLI.
func TestBuiltinClaudeListIsTheCurrentModels(t *testing.T) {
	res := withBuiltin(Result{AgentType: "claude", Models: []Model{}})
	require.Equal(t, "builtin", res.Source)
	got := map[string]string{}
	var order []string
	for _, m := range res.Models {
		got[m.Value] = m.DisplayName
		order = append(order, m.Value)
	}
	assert.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"}, order)
	assert.Equal(t, "Claude Opus 5.5", got["claude-opus-5-5"])
	assert.Equal(t, "Claude Sonnet 5.5", got["claude-sonnet-5-5"])
	assert.Equal(t, "Claude Haiku 5.5", got["claude-haiku-5-5"])
	assert.Equal(t, "Claude Fable 5.1", got["claude-fable-5-1"])
	assert.Equal(t, ThinkingLevels("claude"), res.Thinking)
}

// A staleness guard: every curated Claude model is one the rest of Coral knows (a context window of its
// own, not the default for a model it has never heard of), so a model added here without the pricing
// side, or left behind when the pricing moves on, fails here.
func TestBuiltinClaudeModelsAreKnownToTheCostTable(t *testing.T) {
	for _, id := range at.AgentTypeModels[at.Claude] {
		assert.Equal(t, 1_000_000, proxy.LookupContextWindow(id), id)
		assert.Contains(t, builtinDisplayNames, id, "a curated model has a display name")
	}
}

// A model the curated list does not name is shown by its id.
func TestBuiltinListShowsAnUnnamedModelByItsID(t *testing.T) {
	res := withBuiltin(Result{AgentType: "gemini", Models: []Model{}})
	require.NotEmpty(t, res.Models)
	for _, m := range res.Models {
		assert.Equal(t, m.Value, m.DisplayName)
	}
}

// A signed-out or offline CLI leaves every agy alias on the same curated list (#5909).
func TestFailedAgyAliasFetchUsesFamilyBuiltins(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{"signed out", errors.New("sign in required")},
		{"offline", context.DeadlineExceeded},
	} {
		for _, agentType := range []string{at.Agy, at.Antigravity, at.Gemini} {
			t.Run(failure.name+"/"+agentType, func(t *testing.T) {
				var fetchedFamily string
				s, _ := newTestService(t, fakeCLI(t), func(_ context.Context, family, _ string) ([]Model, string, error) {
					fetchedFamily = family
					return nil, "", failure.err
				})
				s.Get(agentType)
				s.Wait()
				res := s.Get(agentType)
				require.Equal(t, at.Agy, fetchedFamily)
				require.NotEmpty(t, res.Models)
				assert.Equal(t, at.AgentTypeModels[at.Agy], values(res.Models))
				assert.Equal(t, "builtin", res.Source)
				assert.Equal(t, agentType, res.AgentType)
				assert.NotEmpty(t, res.Error)
				assert.False(t, res.Refreshing)
			})
		}
	}
}
