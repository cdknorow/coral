package hubproxy

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3649: only the remote's own key refusal is "key rejected"; a refused Host is its own status;
// every other 401 or 403 is that request's answer and never marks the server.
func TestRefusalTellsKeyHostAndApplicationAnswersApart(t *testing.T) {
	resp := func(code int, ct, refused string) *http.Response {
		h := http.Header{}
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		if refused != "" {
			h.Set(RefusedHeader, refused)
		}
		return &http.Response{StatusCode: code, Header: h}
	}
	for _, tc := range []struct {
		name string
		r    *http.Response
		want error
	}{
		{"marked key refusal", resp(401, "text/plain; charset=utf-8", "key"), ErrRemoteRejected},
		{"bare 401 from an older remote", resp(401, "text/plain; charset=utf-8", ""), ErrRemoteRejected},
		{"application 401 (session_token_required)", resp(401, "application/json", ""), nil},
		{"application 401 with a charset", resp(401, "application/json; charset=utf-8", ""), nil},
		{"marked host refusal", resp(403, "text/plain; charset=utf-8", "host"), ErrHostRefused},
		{"application 403", resp(403, "application/json", ""), nil},
		{"plain 403", resp(403, "text/plain", ""), nil},
		{"200", resp(200, "application/json", ""), nil},
		{"no response", nil, nil},
	} {
		assert.Equal(t, tc.want, Refusal(tc.r), tc.name)
	}
}
