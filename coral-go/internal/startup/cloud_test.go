package startup

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/secretbox"
)

type attacher struct{ m *cloud.Manager }

func (a *attacher) SetCloud(m *cloud.Manager) { a.m = m }

func cloudEnv(t *testing.T, cloudURL string) (*cloud.Manager, *atomic.Int64) {
	t.Helper()
	t.Setenv(secretbox.EnvKey, "")
	hits := &atomic.Int64{}
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) })) // TLS: release builds refuse a cleartext hub
	t.Cleanup(hub.Close)
	if cloudURL == "HUB" {
		cloudURL = hub.URL
	}
	dir := t.TempDir()
	cfg := config.Load(dir)
	cfg.CloudURL = cloudURL
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &attacher{}
	startCloudConnector(ctx, db, cfg, a, store.NewSessionStore(db))
	require.NotNil(t, a.m, "the status route needs a manager even when the connector is off")
	time.Sleep(200 * time.Millisecond) // give any (unwanted) goroutine time to dial
	return a.m, hits
}

func TestConnectorOffByDefaultMakesNoOutboundConnection(t *testing.T) {
	m, hits := cloudEnv(t, "")
	require.Equal(t, cloud.StateOff, m.Status().State)
	require.False(t, m.Status().Configured)
	require.Zero(t, hits.Load())
}

func TestInvalidCloudURLLeavesTheConnectorOff(t *testing.T) {
	for _, bad := range []string{"ftp://hub.example.com", "hub.example.com", "http://hub.example.com", "https://hub.example.com/x"} {
		m, _ := cloudEnv(t, bad)
		require.Equal(t, cloud.StateOff, m.Status().State, bad)
	}
}

// An enrolled server reconnects on its own with the default (no --cloud-connect): the
// switch only governs offering a new connection (#3459).
func TestEnrolledServerConnectsWithoutTheOffer(t *testing.T) {
	t.Setenv(secretbox.EnvKey, "")
	t.Setenv("CORAL_CLOUD_CONNECT", "")
	// Connections, not requests: the test hub's certificate is not trusted, so the dial ends
	// at the handshake; that it is made at all is the point.
	dials := &atomic.Int64{}
	hub := httptest.NewUnstartedServer(http.NotFoundHandler())
	hub.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dials.Add(1)
		}
	}
	hub.Config.ErrorLog = log.New(io.Discard, "", 0)
	hub.StartTLS()
	t.Cleanup(hub.Close)
	dir := t.TempDir()
	cfg := config.Load(dir)
	cfg.CloudURL = hub.URL
	require.False(t, cfg.OffersCloudConnect())
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	u, err := cloud.NormalizeHubURL(hub.URL)
	require.NoError(t, err)
	cs := store.NewCloudStore(db, store.NewRemoteServerStore(db, cfg.CoralDir()))
	require.NoError(t, cs.Save(context.Background(), u, "srv_1", "My Laptop", "coralhub_srv_abcd1234_"+"x0123456789012345678901234567890123456789012"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &attacher{}
	startCloudConnector(ctx, db, cfg, a, store.NewSessionStore(db))
	require.Eventually(t, func() bool { return dials.Load() > 0 }, 5*time.Second, 20*time.Millisecond, "the enrolled server dials its hub")
	require.NotEqual(t, cloud.StateIdle, a.m.Status().State)
}

func TestConfiguredButNotEnrolledDoesNotDialUntilTheUserConnects(t *testing.T) {
	m, hits := cloudEnv(t, "HUB") // dev build: loopback http is accepted
	require.Equal(t, cloud.StateIdle, m.Status().State)
	require.Zero(t, hits.Load(), "configured but not enrolled: no traffic until the user clicks Connect")
}
