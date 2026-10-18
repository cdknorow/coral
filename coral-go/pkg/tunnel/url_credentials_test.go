package tunnel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/secrets"
	"github.com/cdknorow/coral/pkg/redact"
)

// #4769: through a real hub session with the connector's real redactor, a reply holding a URL with credentials reaches a View viewer with the
// password masked (the scheme, the user and the host still show), and an Interact or Launch viewer gets it as it is (their tier, #3958).
func TestURLPasswordIsMaskedForAViewViewerThroughTheTunnel(t *testing.T) {
	pass := fake("", 20, alnum)
	url := "https://bob:" + pass + "@example.com/repo.git"
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":"REMOTE=`+url+`","lines":["connecting to `+url+`"]}`)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.Redactor = secrets.Redactor()
		l.MaxMode = func() Mode { return TierLaunch }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
	})
	get := func(tier string) string {
		t.Helper()
		ctx := context.Background()
		if tier != "" {
			ctx = WithHubTier(ctx, tier)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://x/api/sessions/live/a/capture", nil)
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	for _, tier := range []string{"", "view"} {
		body := get(tier)
		assert.NotContains(t, body, pass, "tier %q: the password is masked: %s", tier, body)
		assert.Equal(t, 2, strings.Count(body, "https://bob:"+redact.Marker+"@example.com/repo.git"), "tier %q: scheme, user and host stay: %s", tier, body)
	}
	for _, tier := range []string{"interact", "launch"} {
		assert.Contains(t, get(tier), pass, "tier %q gets what the computer answered", tier)
	}
}
