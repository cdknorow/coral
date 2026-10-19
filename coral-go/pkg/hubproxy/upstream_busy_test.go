package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

type markRecorder struct{ marks []string }

func (m *markRecorder) Resolve(context.Context, string) (Target, error) {
	return nil, errors.New("unused")
}
func (m *markRecorder) MarkStatus(_ context.Context, id, status, _ string) {
	m.marks = append(m.marks, id+" "+status)
}

// #5035: a terminal or dashboard upgrade the machine's Coral refused with 429 (its cap on open sockets, #5011) is BUSY, not an outage: 503 with the
// Retry-After it gave, the stream cap's code, and the machine is not marked unreachable. Other upstream statuses still are an outage.
func TestUpstream429UpgradeIsBusyAndTheMachineStaysReachable(t *testing.T) {
	rec := &markRecorder{}
	h := New(rec)
	call := func(err error) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.writeUpstreamErr(w, context.Background(), "srv-1", err)
		return w
	}

	w := call(&tunnel.StreamError{Code: tunnel.CodeUpstream, Message: "local websocket failed", Status: http.StatusTooManyRequests, RetryAfter: "7"})
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "7", w.Header().Get("Retry-After"), "the machine's Retry-After is passed on")
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "remote busy", body["error"])
	assert.Equal(t, tunnel.CodeTooManyStreams, body["code"], "the dashboard backs off on the busy code it already knows")
	assert.Empty(t, rec.marks, "the machine answered: it is not marked unreachable")

	w = call(&tunnel.StreamError{Code: tunnel.CodeUpstream, Status: http.StatusTooManyRequests})
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"), "no Retry-After given: a short default")
	assert.Empty(t, rec.marks)

	// any other upstream refusal is still the old outage path
	w = call(&tunnel.StreamError{Code: tunnel.CodeUpstream, Status: http.StatusInternalServerError})
	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, []string{"srv-1 " + StatusUnreachable}, rec.marks)
}
