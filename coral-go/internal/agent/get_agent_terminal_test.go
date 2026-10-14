package agent

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// 'terminal' is a supported agent_type: asking for its agent must not log that the type
// is unknown, while a really unknown type still does (#3259).
func TestGetAgentTerminalIsNotUnknown(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	require.NotNil(t, GetAgent(at.Terminal))
	assert.NotContains(t, buf.String(), "unknown agent type")

	GetAgent("not-a-real-agent")
	assert.Contains(t, buf.String(), "unknown agent type")
}
