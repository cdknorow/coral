package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #6627: a person's held chat message has an id (in the send's answer), is listed on the live row as held_messages, and can be cancelled before it is typed.

type heldRig struct {
	*holdRig
	cancel func(id string) *httptest.ResponseRecorder
	rows   func() []map[string]any
}

func newHeldRig(t *testing.T) *heldRig {
	r := &heldRig{holdRig: newHoldRig(t, true)}
	rt := chi.NewRouter()
	rt.Delete("/api/sessions/live/{name}/held/{id}", r.h.CancelHeld)
	rt.Get("/api/sessions/live", r.h.List)
	r.cancel = func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/api/sessions/live/"+r.name+"/held/"+id, nil)
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		rt.ServeHTTP(rec, req)
		return rec
	}
	r.rows = func() []map[string]any {
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/live", nil))
		var list []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		for _, s := range list {
			if s["session_id"] == r.sid {
				m, _ := s["held_messages"].([]any)
				out := make([]map[string]any, 0, len(m))
				for _, x := range m {
					out = append(out, x.(map[string]any))
				}
				if len(out) == 0 {
					return nil
				}
				return out
			}
		}
		t.Fatal("no live row for the agent")
		return nil
	}
	return r
}

func (r *heldRig) hold(text string) (string, chan error) {
	r.t.Helper()
	settled := make(chan error, 1)
	id, err := r.h.deliverPersonSend(context.Background(), r.name, "claude", r.sid, text, false, func(err error) { settled <- err })
	require.NoError(r.t, err)
	require.NotEmpty(r.t, id)
	return id, settled
}

func TestHeldSendAnswersWithItsIDAndTheRowListsIt(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	rec := r.send("deploy to staging")
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	id, _ := out["held_id"].(string)
	require.NotEmpty(t, id, "the held answer names the message")

	rows := r.rows()
	require.Len(t, rows, 1)
	assert.Equal(t, id, rows[0]["id"])
	assert.Equal(t, "deploy to staging", rows[0]["text"], "the person's own text, for the composer to show")
	assert.Equal(t, false, rows[0]["sending"])
	_, err := time.Parse(time.RFC3339, rows[0]["held_at"].(string))
	assert.NoError(t, err)

	r.answered()
	r.waitTyped(1, 5*time.Second)
	require.Eventually(t, func() bool { return r.rows() == nil }, 5*time.Second, 10*time.Millisecond, "typed: no longer held")
}

func TestARowWithNothingHeldHasANullHeldMessages(t *testing.T) {
	r := newHeldRig(t)
	assert.Nil(t, r.rows())
}

func TestCancelDropsAHeldMessageAndItIsNeverTyped(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	id, settled := r.hold("never mind")
	rec := r.cancel(id)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	select {
	case err := <-settled:
		assert.Equal(t, errSendCancelled, err, "its waiter is told it was cancelled")
	case <-time.After(3 * time.Second):
		t.Fatal("the waiter is settled at once")
	}
	assert.Nil(t, r.rows(), "gone from the row")
	r.answered()
	time.Sleep(6 * personHoldSettle)
	assert.Empty(t, r.typed(), "a cancelled message is never typed")
	assert.Equal(t, http.StatusNotFound, r.cancel(id).Code, "and cancelling it again finds nothing")
}

func TestCancelOneOfSeveralKeepsTheOthersAndTheirOrder(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	_, _ = r.hold("first")
	mid, _ := r.hold("second")
	_, _ = r.hold("third")
	require.Equal(t, http.StatusOK, r.cancel(mid).Code)
	rows := r.rows()
	require.Len(t, rows, 2)
	assert.Equal(t, []any{"first", "third"}, []any{rows[0]["text"], rows[1]["text"]})
	r.answered()
	r.waitTyped(2, 5*time.Second)
	assert.Equal(t, []string{"first", "third"}, r.typed())
}

func TestCancelTheOneWaitingItsTurnLetsTheNextGoOut(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	head, settled := r.hold("head")
	_, _ = r.hold("next")
	time.Sleep(3 * personHoldPoll) // the head is being waited for
	require.Equal(t, http.StatusOK, r.cancel(head).Code)
	assert.Equal(t, errSendCancelled, <-settled)
	r.answered()
	r.waitTyped(1, 5*time.Second)
	assert.Equal(t, []string{"next"}, r.typed())
}

func TestAMessageBeingTypedCannotBeCancelled(t *testing.T) {
	r := newHeldRig(t)
	typing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	r.term.onType = func(string) {
		once.Do(func() { close(typing) })
		<-release
		r.post(`{"event_type":"prompt_submit","summary":"User submitted prompt"}`)
	}
	r.box("permission")
	id, settled := r.hold("on its way")
	r.answered()
	select {
	case <-typing:
	case <-time.After(5 * time.Second):
		t.Fatal("typing started")
	}
	rows := r.rows()
	require.Len(t, rows, 1)
	assert.Equal(t, true, rows[0]["sending"], "the row says it is being sent")
	rec := r.cancel(id)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "already_sending")
	close(release)
	select {
	case err := <-settled:
		assert.NoError(t, err, "it was sent")
	case <-time.After(5 * time.Second):
		t.Fatal("answered")
	}
	assert.Equal(t, []string{"on its way"}, r.typed())
}

func TestCancelFindsOnlyThatAgentsMessage(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	id, _ := r.hold("mine")
	assert.Equal(t, http.StatusNotFound, r.cancel("h0000000000000000").Code, "an unknown id")
	err := r.h.cancelHeld("claude-bbbbbbbb-6610-4000-8000-000000000009", id, false)
	assert.Equal(t, errHeldNone, err, "another agent's name does not reach it")
	assert.NotNil(t, r.rows(), "still held")
}

func TestAHubSenderCannotCancelADashboardMessage(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission")
	dash, _ := r.hold("from the dashboard")
	hubID, err := r.h.deliverPersonSend(context.Background(), r.name, "claude", r.sid, "from the hub", true, nil)
	require.NoError(t, err)
	assert.Equal(t, errHeldNotYours, r.h.cancelHeld(r.name, dash, true), "Coral Hub does not cancel what the computer's own dashboard sent")
	assert.NoError(t, r.h.cancelHeld(r.name, hubID, true), "a Hub sender cancels one sent through the Hub")
	assert.NoError(t, r.h.cancelHeld(r.name, dash, false), "the dashboard of this computer may cancel any")
}

// Who may cancel: the route is Interact through Coral Hub (a viewer who can only look cannot cancel, as it cannot send), and Operator-only for a token (identity_routes.go).
func TestCancelIsInteractThroughTheHub(t *testing.T) {
	_, tier, ok := tunnel.RuleFor(http.MethodDelete, "/api/sessions/live/x/held/h1")
	require.True(t, ok)
	assert.Equal(t, tunnel.TierInteract, tier)
	_, ok = operatorOnlyRoutes["DELETE /api/sessions/live/{name}/held/{id}"]
	assert.True(t, ok, "an agent's token is refused")
}

// A cancelled message's wait ends at once: the agent's release loop does not go on waiting for a box on its behalf.
func TestCancelEndsTheWaitAtOnce(t *testing.T) {
	r := newHeldRig(t)
	r.box("permission") // stays open
	id, _ := r.hold("alone")
	require.True(t, r.h.holds.waiting(personKey(r.name, r.sid)))
	require.Equal(t, http.StatusOK, r.cancel(id).Code)
	require.Eventually(t, func() bool { return !r.h.holds.waiting(personKey(r.name, r.sid)) }, 2*time.Second, 5*time.Millisecond, "the loop ended while the box is still open")
}
