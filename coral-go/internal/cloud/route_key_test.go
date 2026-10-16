package cloud

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3683: the connector keeps the route key the hub gave and sends it back on its next dial to
// that hub; nothing on the first dial, and the key is never logged.
func TestConnectorSendsTheHubsRouteKeyBack(t *testing.T) {
	const key = "fedcba9876543210fedcba9876543210"
	e := newEnv(t)
	e.hub.routeKey.Store(key)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	s1 := e.hub.session(t)
	waitState(t, m, StateConnected)
	require.Eventually(t, func() bool {
		k, _ := e.cs.RouteKey(context.Background(), e.hub.srv.URL)
		return k == key
	}, 5*time.Second, 10*time.Millisecond, "kept for this hub")

	s1.Close() // the hub drops the tunnel: the connector dials again
	e.hub.session(t)
	assert.Equal(t, []string{"", key}, e.hub.routeHeaders(), "none on the first dial, the key on the next")
	assert.NotContains(t, e.logs.String(), key, "never logged")
}
