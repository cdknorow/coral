//go:build !dev

package cloud

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Release builds never accept cleartext, even for loopback: TLS verification cannot be switched off.
func TestReleaseBuildRequiresHTTPS(t *testing.T) {
	for _, bad := range []string{"http://127.0.0.1:8450", "http://localhost:8450", "http://[::1]:8450"} {
		_, err := NormalizeHubURL(bad)
		require.Error(t, err, bad)
	}
}
