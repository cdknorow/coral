package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/redact"
)

// #3763 (secrets audit #3762 finding 1): an agent event is redacted once, where it is stored: a
// tool call's summary and detail hold the whole command, and sessions.db is what gets backed up
// and attached. An ordinary command is stored as it was. Since #3808 the rules are betterleaks'
// (internal/secrets): these secrets are ones it finds.
func TestAgentEventsAreStoredRedacted(t *testing.T) {
	ts := NewTaskStore(openTestDB(t))
	ctx := context.Background()
	sid := "s1"
	gh := "ghp_" + "R8kd2LqP0vXz7TnWb4YcHs9JmQe1Ua6GfKo3"
	pw := "Zq8" + "rT2vLm9XkP4sWn7B"
	cmd := "git push https://x:" + gh + "@github.com/org/repo && psql postgres://admin:" + pw + "@db.example.test/app"
	detail, err := json.Marshal(map[string]any{"command": cmd, "description": "deploy"})
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
	for _, s := range []string{gh, pw} {
		assert.NotContains(t, secret.Summary, s, "summary")
		assert.NotContains(t, *secret.DetailJSON, s, "detail")
	}
	assert.Contains(t, secret.Summary, "Ran: git push https://", "the command around the secrets stays")
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(*secret.DetailJSON), &got), "the detail is still JSON")
	assert.Equal(t, "deploy", got["description"])

	ordinary := byPlain[true]
	assert.Equal(t, "Ran: "+plainCmd, ordinary.Summary, "an ordinary command is stored unchanged")
	assert.Equal(t, p, *ordinary.DetailJSON)
}

// An event the redactor can't check is not stored as it was: a note in place of its summary, no
// detail.
func TestAnUncheckedEventIsNotStored(t *testing.T) {
	prev := eventRedactor
	eventRedactor = func() redact.Redactor { return redact.Refuse }
	t.Cleanup(func() { eventRedactor = prev })
	ts := NewTaskStore(openTestDB(t))
	ctx := context.Background()
	sid := "s1"
	d := `{"command":"anything"}`
	_, err := ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "dev", SessionID: &sid, EventType: "tool_use", Summary: "Ran: anything", DetailJSON: &d})
	require.NoError(t, err)
	events, err := ts.ListAgentEvents(ctx, "dev", 10, &sid)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, uncheckedEvent, events[0].Summary)
	assert.Nil(t, events[0].DetailJSON)
}
