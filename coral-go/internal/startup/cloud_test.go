package startup

import (
	"context"
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
	db, err := store.Open(filepath.Join(dir, "sessions.db"))
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

func TestConfiguredButNotEnrolledDoesNotDialUntilTheUserConnects(t *testing.T) {
	m, hits := cloudEnv(t, "HUB") // dev build: loopback http is accepted
	require.Equal(t, cloud.StateIdle, m.Status().State)
	require.Zero(t, hits.Load(), "configured but not enrolled: no traffic until the user clicks Connect")
}
