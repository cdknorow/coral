package background

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #3649: a remote that refuses the Host the hub uses for it (its DNS-rebinding check, through a
// port forward under another name) is shown as host_refused, never "key rejected"; a remote's
// application answer (a JSON 401 or any other 403) to the worker's probe is not a key failure.
func TestHub_HostRefusedIsItsOwnStatus(t *testing.T) {
	reg := newFakeRegistry()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Coral-Refused", "host")
		http.Error(w, "Forbidden", 403)
	}))
	defer host.Close()
	reg.put("fwd", host.URL)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"x","code":"session_token_required"}`))
	}))
	defer app.Close()
	reg.put("app", app.URL)
	h := fastHub(reg)
	startHub(t, h)

	require.Eventually(t, func() bool {
		m := statusMap(h.Snapshot())
		return m["fwd"] == "host_refused" && m["app"] == "unreachable"
	}, 3*time.Second, 20*time.Millisecond, "fwd host_refused, app not unauthorized")
}
