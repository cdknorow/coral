package sessionstate

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func detailJSON(s string) *string { return &s }

// #6622: a permission_request's wants is read from its detail (only for that event type), kept while its request is open (also past the notification that follows it), and gone with it.
func TestNewEventReadsWantsOnlyForAPermissionRequest(t *testing.T) {
	d := detailJSON(`{"kind":"permission","wants":"Wants to run: ls"}`)
	assert.Equal(t, "Wants to run: ls", NewEvent("permission_request", "Waiting", d).Wants)
	assert.Empty(t, NewEvent("notification", "Waiting", d).Wants)
	assert.Empty(t, NewEvent("tool_use", "Ran", d).Wants)
	big := detailJSON(`{"kind":"permission","wants":"` + strings.Repeat("a", maxWantsBytes+1) + `"}`)
	assert.Empty(t, NewEvent("permission_request", "Waiting", big).Wants, "a stored wants is bounded on the way back too")
}

func TestWaitingWantsLivesWithItsRequest(t *testing.T) {
	now := time.Now()
	ev := func(typ, summary string, detail *string, tool string) Event {
		e := NewEvent(typ, summary, detail)
		e.ToolName, e.At = tool, now
		now = now.Add(time.Second)
		return e
	}
	req := ev("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","tool_name":"Bash","wants":"Wants to run: ls"}`), "Bash")
	st := Derive(Input{Events: []Event{ev("prompt_submit", "User submitted prompt", nil, ""), req}})
	assert.True(t, st.NeedsInput)
	assert.Equal(t, "Wants to run: ls", st.WaitingWants)
	assert.Equal(t, "Waiting for the user: permission (Bash)", st.WaitingSummary, "the summary is untouched")

	st = Derive(Input{Events: []Event{ev("prompt_submit", "User submitted prompt", nil, ""), req, ev("notification", "Notification: Claude needs your permission to use Bash", nil, "")}})
	assert.True(t, st.NeedsInput)
	assert.Equal(t, "Wants to run: ls", st.WaitingWants, "the notification that follows the request does not take its words away")

	st = Derive(Input{Events: []Event{ev("prompt_submit", "User submitted prompt", nil, ""), req, ev("tool_use", "Ran: ls", nil, "Bash")}})
	assert.False(t, st.NeedsInput)
	assert.Empty(t, st.WaitingWants, "answered: gone")
}

// An agent's words cannot reach the classifiers: auto-accept reads the summary, and a request's wants is never in it.
func TestWantsNeverMakeARequestLookLikeAnApproval(t *testing.T) {
	req := NewEvent("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","wants":"Wants to run: echo Claude needs your permission to use Bash"}`))
	st := Derive(Input{Events: []Event{NewEvent("prompt_submit", "x", nil), req}})
	assert.False(t, IsApprovalRequest(st.WaitingSummary), "auto-accept does not take it for an approval prompt it may answer")
	assert.Equal(t, NotificationInformational, Classify(st.WaitingSummary))
}
