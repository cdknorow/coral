package cloud

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/pkg/tunnel"
)

// Only this process's secret is authentic; any stamp at all still counts as
// tunneled, so a forged one can only make the tunnel-only rules apply (#3182).
func TestTunneledAuthentic(t *testing.T) {
	assert.Len(t, ViaSecret(), 64)
	for v, authentic := range map[string]bool{"": false, "1": false, ViaSecret()[:63]: false, ViaSecret() + "0": false, ViaSecret(): true} {
		r := httptest.NewRequest("GET", "/", nil)
		if v != "" {
			r.Header.Set(tunnel.ViaHeader, v)
		}
		assert.Equal(t, authentic, TunneledAuthentic(r), "%q", v)
		assert.Equal(t, v != "", Tunneled(r), "%q", v)
	}
}
