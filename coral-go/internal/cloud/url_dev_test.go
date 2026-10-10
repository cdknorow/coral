//go:build dev

package cloud

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Dev builds (tests, local development) accept cleartext for loopback only.
func TestDevBuildAllowsLoopbackHTTPOnly(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8450", "http://localhost:8450", "http://[::1]:8450"} {
		_, err := NormalizeHubURL(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{"http://hub.example.com", "http://10.0.0.1:8450", "http://127.0.0.1.evil.example"} {
		_, err := NormalizeHubURL(bad)
		require.Error(t, err, bad)
	}
}
