package background

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// flakyRemote answers the hub's list and health checks, or fails them all (a dropped port
// forward) while down is set.
func flakyRemote(t *testing.T, down *atomic.Bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"status":"ok","version":"1.4.48"}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func graceHub(reg *fakeRegistry, grace time.Duration) *RemoteAgentHub {
	return NewRemoteAgentHub(reg, regResolver{reg}, RemoteAgentHubConfig{
		PollInterval: 30 * time.Millisecond, RequestTimeout: 300 * time.Millisecond,
		RegistryRefresh: 50 * time.Millisecond, MinBackoff: 10 * time.Millisecond, MaxBackoff: 30 * time.Millisecond,
		OfflineGrace: grace,
	})
}

// #3649: a connection that drops and comes back within the grace never shows the server offline
// (the operator's flapping over an SSH port forward).
func TestHub_BriefDropNeverFlashesOffline(t *testing.T) {
	var down atomic.Bool
	reg := newFakeRegistry()
	reg.put("fwd", flakyRemote(t, &down).URL)
	h := graceHub(reg, 600*time.Millisecond)
	startHub(t, h)
	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["fwd"] == store.RemoteStatusOnline }, 3*time.Second, 10*time.Millisecond)

	for round := 0; round < 3; round++ {
		down.Store(true)
		end := time.Now().Add(200 * time.Millisecond) // several failed probes, well inside the grace
		for time.Now().Before(end) {
			assert.Equal(t, store.RemoteStatusOnline, statusMap(h.Snapshot())["fwd"], "round %d: no flash during a brief drop", round)
			time.Sleep(10 * time.Millisecond)
		}
		down.Store(false)
		time.Sleep(150 * time.Millisecond) // a good probe ends the run, so the next drop starts afresh
	}
	assert.Equal(t, store.RemoteStatusOnline, statusMap(h.Snapshot())["fwd"])
	assert.Equal(t, store.RemoteStatusOnline, reg.statusOf("fwd"), "nor in the registry")
}

// A real outage still shows, once it has lasted the grace; a proxied failure inside the grace
// doesn't flip the status either.
func TestHub_LongOutageShowsAfterTheGrace(t *testing.T) {
	var down atomic.Bool
	reg := newFakeRegistry()
	reg.put("fwd", flakyRemote(t, &down).URL)
	const grace = 400 * time.Millisecond
	h := graceHub(reg, grace)
	startHub(t, h)
	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["fwd"] == store.RemoteStatusOnline }, 3*time.Second, 10*time.Millisecond)

	down.Store(true)
	start := time.Now()
	assert.True(t, h.ReportFailure(context.Background(), "fwd", store.RemoteStatusUnreachable, "connect failed"))
	assert.Equal(t, store.RemoteStatusOnline, statusMap(h.Snapshot())["fwd"], "a proxied failure inside the grace")
	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["fwd"] == store.RemoteStatusUnreachable }, 3*time.Second, 10*time.Millisecond)
	assert.GreaterOrEqual(t, time.Since(start), grace-50*time.Millisecond, "not before the grace")

	down.Store(false)
	require.Eventually(t, func() bool { return statusMap(h.Snapshot())["fwd"] == store.RemoteStatusOnline }, 3*time.Second, 10*time.Millisecond, "and back")
}
