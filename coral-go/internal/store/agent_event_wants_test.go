package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEventJSONDropsWhatARequestWants(t *testing.T) {
	d := `{"kind":"permission","tool_name":"Bash","wants":"Wants to run: ls"}`
	b, err := json.Marshal(AgentEvent{ID: 1, AgentName: "a", EventType: "permission_request", Summary: "Waiting", DetailJSON: &d})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "wants")
	assert.NotContains(t, string(b), "ls")
	assert.Contains(t, string(b), "permission", "the kind and tool stay")
	assert.Equal(t, "Wants to run: ls", mustWants(t, d), "the stored detail is untouched")

	other := `{"wants":"a tool's own field"}`
	b, err = json.Marshal(AgentEvent{EventType: "tool_use", DetailJSON: &other})
	require.NoError(t, err)
	assert.Contains(t, string(b), "a tool's own field", "only a permission_request's wants is hidden")

	b, err = json.Marshal(AgentEvent{EventType: "permission_request"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "detail_json")
}

func mustWants(t *testing.T, detail string) string {
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(detail), &m))
	return m["wants"].(string)
}
