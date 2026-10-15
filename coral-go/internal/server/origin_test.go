package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The CORS rule allows only this server's own origin (#3511): no other port of
// localhost, whatever the request's Host.
func TestCORSAllowsOnlyTheServersOwnOrigin(t *testing.T) {
	for _, c := range []struct {
		origin, host string
		want         bool
	}{
		{"http://localhost:8450", "localhost:8450", true},
		{"http://127.0.0.1:8450", "127.0.0.1:8450", true},
		{"http://192.168.1.5:8450", "192.168.1.5:8450", true}, // remote access by IP
		{"https://myserver.local:8450", "myserver.local:8450", true},
		{"http://LOCALHOST:8450", "localhost:8450", true},
		{"http://localhost:3000", "localhost:8450", false}, // another localhost port
		{"http://127.0.0.1:5173", "127.0.0.1:8450", false},
		{"http://localhost:8450", "127.0.0.1:8450", false}, // a different host name
		{"http://localhost:8450", "192.168.1.5:8450", false},
		{"http://evil.com", "192.168.1.5:8450", false},
		{"http://localhost.evil.com:8450", "localhost:8450", false},
		{"null", "localhost:8450", false},
		{"", "localhost:8450", false},
	} {
		r, _ := http.NewRequest("GET", "http://"+c.host+"/api/health", nil)
		r.Host = c.host
		assert.Equal(t, c.want, corsSameOrigin(r, c.origin), "%q on %q", c.origin, c.host)
	}
}
