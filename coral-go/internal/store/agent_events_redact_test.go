package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3763 (secrets audit #3762 finding 1): an agent event is redacted once, where it is stored: a
// tool call's summary and detail hold the whole command, and sessions.db is what gets backed up
// and attached. An ordinary command is stored as it was.
func TestAgentEventsAreStoredRedacted(t *testing.T) {
	ts := NewTaskStore(openTestDB(t))
	ctx := context.Background()
	sid := "s1"
	bearer := "abcdefghijklmnopqrstuvwxyz0123456789"
	cmd := "curl -H 'Authorization: Bearer " + bearer + "' https://api.example.test && " +
		"psql postgres://admin:s3cretpw@db.example.test/app && mysql --password=hunter2 x && export TOKEN=abc123def"
	detail, err := json.Marshal(map[string]any{"command": cmd, "description": "deploy", "api_key": "plain words"})
	require.NoError(t, err)
	d := string(detail)
	_, err = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "dev", SessionID: &sid, EventType: "tool_use", Summary: "Ran: " + cmd, DetailJSON: &d})
	require.NoError(t, err)

	plainCmd := `go test ./... && grep -rn "token" internal | head`
	plain, _ := json.Marshal(map[string]any{"command": plainCmd})
	p := string(plain)
	_, err = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "dev", SessionID: &sid, EventType: "tool_use", Summary: "Ran: " + plainCmd, DetailJSON: &p})
	require.NoError(t, err)

	events, err := ts.ListAgentEvents(ctx, "dev", 10, &sid)
	require.NoError(t, err)
	require.Len(t, events, 2)
	byPlain := map[bool]AgentEvent{}
	for _, e := range events {
		byPlain[strings.Contains(e.Summary, "go test")] = e
	}
	secret := byPlain[false]
	for _, s := range []string{bearer, "s3cretpw", "hunter2", "abc123def"} {
		assert.NotContains(t, secret.Summary, s, "summary")
		assert.NotContains(t, *secret.DetailJSON, s, "detail")
	}
	assert.Contains(t, secret.Summary, "Ran: curl -H 'Authorization: Bearer ", "the command around the secrets stays")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(*secret.DetailJSON), &got), "the detail is still JSON")
	assert.Equal(t, "deploy", got["description"])
	assert.NotEqual(t, "plain words", got["api_key"], "a value under a secret-named key, whatever it is")

	ordinary := byPlain[true]
	assert.Equal(t, "Ran: "+plainCmd, ordinary.Summary, "an ordinary command is stored unchanged")
	assert.Equal(t, p, *ordinary.DetailJSON)
}
