package hubproxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Pooled hub->remote connections outlive a quiet spell: the remote poller's
// health check (5s, backing off to 30s) keeps one warm, and a chat switch
// after a pause should not pay a fresh TCP/TLS handshake to the remote.
func TestTransportKeepsIdleConnectionsWarm(t *testing.T) {
	for _, allowPrivate := range []bool{false, true} {
		tr := transport(allowPrivate)
		assert.GreaterOrEqual(t, tr.IdleConnTimeout, 5*time.Minute, "allowPrivate=%v", allowPrivate)
		assert.False(t, tr.DisableKeepAlives, "allowPrivate=%v", allowPrivate)
		assert.Same(t, tr, transport(allowPrivate), "one shared pool per mode")
	}
}
