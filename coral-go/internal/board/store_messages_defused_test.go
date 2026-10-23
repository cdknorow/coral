package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PostDefusedMessage keeps the zero-width space DefuseMentions put after an @ (an ordinary post cleans it away, which would undo the defusing).
func TestPostDefusedMessageKeepsTheDefusing(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	text := "@Dev quoted: " + DefuseMentions("ask @Orchestrator", nil)
	a, err := s.PostDefusedMessage(ctx, "p", "Operator", text)
	require.NoError(t, err)
	assert.Equal(t, text, a.Content)
	assert.Contains(t, a.Content, "@​Orchestrator")
	b, err := s.PostMessageWithOrigin(ctx, "p", "Operator", text, nil, "")
	require.NoError(t, err)
	assert.NotContains(t, b.Content, "​", "an ordinary post cleans it, which is why the defused path exists")
}
