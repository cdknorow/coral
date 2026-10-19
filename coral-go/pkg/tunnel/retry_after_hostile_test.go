package tunnel

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// #5035 review: the hub cleans a Retry-After itself, whatever the connector sent. The connector is the user's machine; a modified one
// (or a stream written by hand) can put any value in the refusal's headers, and the hub repeats it to a browser as Retry-After.
// Mutation: drop CleanRetryAfter in Session.readResponse: the value passes as the connector wrote it.
func TestHubCleansARetryAfterTheConnectorDidNotClean(t *testing.T) {
	for _, c := range []struct{ sent, want string }{{"999999", "60"}, {"soon", ""}, {" 9 ", "9"}, {"Wed, 21 Oct 2026 07:28:00 GMT", ""}, {"-1", ""}} {
		hub := newHub(t, fastCfg())
		cs, err := Dial(context.Background(), hub.URL(), testToken, ClientOptions{
			SessionConfig: hub.Cfg, CoralVersion: "test", AllowInsecure: true,
			Handler: HandlerFunc(func(_ context.Context, s *Stream) {
				_ = s.send(ResponseHeader{Error: "local websocket failed", ErrorCode: CodeUpstream, Status: http.StatusTooManyRequests,
					Headers: http.Header{"Retry-After": {c.sent}}})
			}),
		})
		require.NoError(t, err)
		t.Cleanup(func() { cs.Close() })
		sess := hub.next(t)
		_, err = sess.DialWS(context.Background(), "/ws/terminal/a", "", nil)
		var se *StreamError
		require.ErrorAs(t, err, &se, c.sent)
		require.Equal(t, c.want, se.RetryAfter, "Retry-After %q", c.sent)
	}
}
