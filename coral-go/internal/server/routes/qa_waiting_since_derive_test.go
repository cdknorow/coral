package routes

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/sessionstate"
)

// QA #6985: waiting_since, taken through Derive, is when the agent's own request began, also when Coral's held notice was open first.
func TestWaitingSinceThroughDerive(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	for _, c := range []struct {
		name   string
		events []sessionstate.Event
		want   any
	}{
		{"a request after an old stop", []sessionstate.Event{
			{Type: "stop", At: at(0)}, {Type: "permission_request", Kind: "permission", ToolName: "Bash", At: at(180)}}, at(180).Format(time.RFC3339)},
		{"a second request while the first is open keeps the first's start", []sessionstate.Event{
			{Type: "stop", At: at(0)}, {Type: "permission_request", Kind: "permission", ToolName: "Bash", At: at(10)},
			{Type: "permission_request", Kind: "permission", ToolName: "Edit", At: at(20)}}, at(10).Format(time.RFC3339)},
		{"the agent's own request that follows Coral's held notice begins when it is made", []sessionstate.Event{
			{Type: "stop", At: at(0)}, {Type: "notification", Summary: sessionstate.HeldInputSummary, At: at(10)},
			{Type: "permission_request", Kind: "permission", ToolName: "Bash", At: at(60)}}, at(60).Format(time.RFC3339)},
		{"after the request is answered", []sessionstate.Event{
			{Type: "permission_request", Kind: "permission", ToolName: "Bash", At: at(10)}, {Type: "prompt_submit", At: at(11)}}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, waitingSince(sessionstate.Derive(sessionstate.Input{Events: c.events})))
		})
	}
}
