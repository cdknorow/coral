package peercred

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #95 (Security's conditions 1 and 5): only a positive answer is Self. Another user is OtherUser;
// no row after one re-read 20 ms later, two owners, a process that can't be opened or any other
// error is Unattributed (fail closed); a table layout this build doesn't know is Unsupported.
func TestAttribute(t *testing.T) {
	if !Supported {
		v, err := Attribute(netip.MustParseAddrPort("127.0.0.1:50000"), netip.MustParseAddrPort("127.0.0.1:8420"))
		assert.Equal(t, Unsupported, v)
		assert.NoError(t, err)
		return
	}
	oldCheck, oldWait := ownerCheck, retryWait
	t.Cleanup(func() { ownerCheck, retryWait = oldCheck, oldWait })
	retryWait = 30 * time.Millisecond
	client, server := netip.MustParseAddrPort("127.0.0.1:50000"), netip.MustParseAddrPort("127.0.0.1:8420")

	for _, tc := range []struct {
		name    string
		answers []error // one per table read; nil means a row was found
		me      bool
		want    Verdict
	}{
		{"same user", []error{nil}, true, Self},
		{"another user", []error{nil}, false, OtherUser},
		{"no row, then one on the re-read", []error{ErrNotFound, nil}, true, Self},
		{"no row twice", []error{ErrNotFound, ErrNotFound}, true, Unattributed},
		{"two owners", []error{errAmbiguous}, true, Unattributed},
		{"process can't be opened", []error{errors.New("open process 1234: Access is denied")}, true, Unattributed},
		{"unknown table layout", []error{fmt.Errorf("%w: an inpcb item of 112 bytes", ErrLayout)}, true, Unsupported},
	} {
		reads := 0
		var times []time.Time
		ownerCheck = func(netip.AddrPort, netip.AddrPort) (bool, error) {
			times = append(times, time.Now())
			err := tc.answers[reads]
			reads++
			return tc.me && err == nil, err
		}
		v, _ := Attribute(client, server)
		assert.Equal(t, tc.want, v, tc.name)
		assert.LessOrEqual(t, reads, 2, "%s: at most one re-read", tc.name)
		if len(times) == 2 {
			assert.GreaterOrEqual(t, times[1].Sub(times[0]), retryWait, "%s: the re-read waits", tc.name)
		}
	}
}
