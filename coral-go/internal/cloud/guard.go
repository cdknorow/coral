package cloud

import (
	"net/http"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// TunnelHeader is added by the connector to every request it forwards. The server
// re-checks marked requests (Guard) and routes that manage the connector refuse them.
// The hub cannot forge or remove it: hub headers are scrubbed to an allowlist and this
// one is set afterwards.
const TunnelHeader = "X-Coral-Tunnel"

// Tunneled reports whether the request came through the Cloud Hub tunnel.
func Tunneled(r *http.Request) bool { return r.Header.Get(TunnelHeader) != "" }

// Guard is the server-side half of the route policy. The connector already refuses
// anything outside the shared route table (pkg/tunnel) or above the local maximum
// mode before it reaches the local API; Guard repeats the decision, query string
// included, for every request that carries TunnelHeader, so the policy holds even if
// the connector's own check were bypassed or had a bug. Requests without the header
// (the local dashboard, API keys, agents) are untouched.
func Guard(maxMode func() tunnel.Mode) func(http.Handler) http.Handler {
	allow := tunnel.AllowRequest(maxMode)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if Tunneled(r) && !allow(r.Method, r.URL.Path, r.URL.RawQuery) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"not available through the cloud hub in this access mode"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
