package hubproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryTarget records the query each DialWS is given (a tunnel target passes it on as is).
type queryTarget struct{ queries []string }

func (q *queryTarget) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}
func (q *queryTarget) DialWS(_ context.Context, _, rawQuery string, _ []string) (WSConn, error) {
	q.queries = append(q.queries, rawQuery)
	return nil, errors.New("recorded")
}

// #3356: the browser's api_key never reaches the target of a websocket, whatever the target.
func TestDialWebSocketStripsTheBrowsersAPIKey(t *testing.T) {
	q := &queryTarget{}
	_, err := DialWebSocket(context.Background(), q, "/ws/terminal/a", "session_id=s1&api_key=SECRET&agent_type=claude", nil)
	require.Error(t, err)
	require.Len(t, q.queries, 1)
	assert.NotContains(t, q.queries[0], "SECRET")
	assert.NotContains(t, q.queries[0], "api_key")
	assert.Contains(t, q.queries[0], "session_id=s1")
	assert.Contains(t, q.queries[0], "agent_type=claude")
}
