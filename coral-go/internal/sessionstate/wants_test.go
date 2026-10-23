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

// #6672: a transcript record of the turn going on (turn_activity) keeps an open turn open but never closes a request; with no request it works like a prompt.
func TestTurnActivityNeverClosesAnOpenRequestButKeepsATurnOpen(t *testing.T) {
	now := time.Now()
	ev := func(typ, summary string, detail *string, tool string) Event {
		e := NewEvent(typ, summary, detail)
		e.ToolName, e.At = tool, now
		now = now.Add(time.Second)
		return e
	}
	req := ev("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","tool_name":"Bash"}`), "Bash")
	st := Derive(Input{Events: []Event{ev("prompt_submit", "go", nil, ""), req, ev("turn_activity", "", nil, "")}})
	assert.True(t, st.NeedsInput, "the request stays open")
	assert.Equal(t, "permission", st.WaitingKind)

	st = Derive(Input{Events: []Event{ev("prompt_submit", "go", nil, ""), req, ev("turn_activity", "", nil, ""), ev("tool_use", "Ran", nil, "Bash")}})
	assert.False(t, st.NeedsInput, "its tool's own PostToolUse closes it")

	st = Derive(Input{Events: []Event{ev("prompt_submit", "go", nil, ""), ev("stop", "done", nil, ""), ev("turn_activity", "", nil, "")}})
	assert.True(t, st.Working, "with no request it is the turn going on, as a prompt is")
	assert.False(t, st.AwaitingUser)
}

// #6687: agents run tool calls in parallel and a PermissionRequest hook carries no call id, so the server writes into the request the ids of the calls it may be about (call_ids). A PostToolUse
// answers the request only through one of them, and the request closes when every one has finished: a parallel call of the same tool that finishes first does not close it.
func TestARequestWithItsCallsIsAnsweredOnlyWhenAllOfThemFinished(t *testing.T) {
	now := time.Now()
	ev := func(typ, summary string, detail *string, tool, callID string) Event {
		e := NewEvent(typ, summary, detail)
		e.ToolName, e.At = tool, now
		if typ == "tool_use" {
			e.ToolUseID = callID
		}
		now = now.Add(time.Second)
		return e
	}
	one := ev("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","tool_name":"Bash","call_ids":["call-a"]}`), "Bash", "")
	assert.Equal(t, []string{"call-a"}, one.CallIDs, "read from the request's detail")
	start := ev("prompt_submit", "go", nil, "", "")

	st := Derive(Input{Events: []Event{start, one, ev("tool_use", "Ran: ls", nil, "Bash", "call-b")}})
	assert.True(t, st.NeedsInput, "another Bash call finishing does not answer it")
	st = Derive(Input{Events: []Event{start, one, ev("tool_use", "Ran: ls", nil, "Bash", "call-b"), ev("tool_use", "Ran: rm", nil, "Bash", "call-a")}})
	assert.False(t, st.NeedsInput, "its own call's PostToolUse does")

	// Two parallel calls of the same tool, only one of which waits: the server cannot tell which, so the request waits for both. Either finishing first leaves it open; the second closes it.
	two := ev("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","tool_name":"Bash","call_ids":["call-1","call-2"]}`), "Bash", "")
	for _, first := range [][2]string{{"call-1", "call-2"}, {"call-2", "call-1"}} {
		st = Derive(Input{Events: []Event{start, two, ev("tool_use", "Ran", nil, "Bash", first[0])}})
		assert.True(t, st.NeedsInput, "the first call to finish (%s) leaves it open", first[0])
		st = Derive(Input{Events: []Event{start, two, ev("tool_use", "Ran", nil, "Bash", first[0]), ev("tool_use", "Ran", nil, "Bash", first[1])}})
		assert.False(t, st.NeedsInput, "the second closes it")
	}
	st = Derive(Input{Events: []Event{start, two, ev("tool_use", "Ran", nil, "Bash", "call-3")}})
	assert.True(t, st.NeedsInput, "a call it does not wait for changes nothing")

	// A request whose calls are not known is answered by the tool's name, as before.
	anon := ev("permission_request", "Waiting for the user: permission (Bash)", detailJSON(`{"kind":"permission","tool_name":"Bash"}`), "Bash", "")
	st = Derive(Input{Events: []Event{start, anon, ev("tool_use", "Ran: ls", nil, "Bash", "call-b")}})
	assert.False(t, st.NeedsInput)

	// One tied request and one not: the id logic does not apply (not every open request has its calls), so the name answers one.
	st = Derive(Input{Events: []Event{start, one, anon, ev("tool_use", "Ran: ls", nil, "Bash", "call-b")}})
	assert.True(t, st.NeedsInput, "one request is left open")
}
