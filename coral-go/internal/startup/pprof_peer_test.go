//go:build dev

package startup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/peercred"
)

// #95: the profiler has no credential, so it answers only Coral's own OS user.
func TestProfilerIsOnlyForCoralsOwnUser(t *testing.T) {
	verdict := peercred.OtherUser
	t.Cleanup(auth.SetPeerAttribution(func(netip.AddrPort, netip.AddrPort) (peercred.Verdict, error) { return verdict, nil }))
	h := ownUserOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tc := range []struct {
		v    peercred.Verdict
		want int
	}{{peercred.OtherUser, http.StatusForbidden}, {peercred.Unattributed, http.StatusForbidden}, {peercred.Self, http.StatusOK}, {peercred.Unsupported, http.StatusOK}} {
		verdict = tc.v
		ctx := auth.ConnContext(context.Background(), loopbackConn{})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/heap", nil).WithContext(ctx))
		assert.Equal(t, tc.want, rec.Code, "verdict %d", tc.v)
	}
}
