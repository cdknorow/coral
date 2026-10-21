package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/config"
)

// Security's note on df75d9aa: with TLS on its port, the loopback origins Coral registers with
// Coral Hub include https, so an https dashboard on this computer gets the Hub button. Each is a
// valid, canonical return origin.
func TestLoopbackOriginsFollowTheListener(t *testing.T) {
	plain := (&Server{cfg: &config.Config{Port: 8420}}).loopbackOrigins()
	assert.Equal(t, []string{"http://localhost:8420", "http://127.0.0.1:8420", "http://[::1]:8420"}, plain)

	tls := (&Server{cfg: &config.Config{Port: 8420, TLS: true}}).loopbackOrigins()
	assert.Equal(t, []string{
		"http://localhost:8420", "http://127.0.0.1:8420", "http://[::1]:8420",
		"https://localhost:8420", "https://127.0.0.1:8420", "https://[::1]:8420",
	}, tls)
	for _, o := range tls {
		c, err := auth.CanonicalOrigin(o)
		assert.NoError(t, err, o)
		assert.Equal(t, o, c, "already canonical")
	}
}
