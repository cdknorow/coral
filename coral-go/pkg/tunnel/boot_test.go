package tunnel

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #5855: the connector's Hello carries this process's boot id and start time, so the hub can tell a restart (what Coral keeps in memory is gone) from a reconnect.
func TestTheHubReadsThePeersBootIdentity(t *testing.T) {
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), BootID(), "random, 16 hex digits")
	assert.Equal(t, BootID(), BootID(), "the same for the life of the process")
	hub := newHub(t, fastCfg())
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	one, _ := connect(t, hub, ok, nil)
	two, _ := connect(t, hub, ok, nil)
	assert.Equal(t, BootID(), one.PeerBootID(), "the hub reads the id the connector sent")
	assert.Equal(t, one.PeerBootID(), two.PeerBootID(), "two connections of one process: one id")
	assert.WithinDuration(t, thisBoot.at, one.PeerStartedAt(), time.Second, "and when the process started (to the second)")
	assert.True(t, one.PeerStartedAt().Before(time.Now().Add(time.Second)))
}

// Malformed values read as absent; they never reject the connector (the pair is informational).
func TestAMalformedBootIdentityReadsAsNone(t *testing.T) {
	for _, id := range []string{"", "XYZ", "ABCDEF0123456789", "0123456789abcdef0123456789abcdef0", "0123 4567", "0123456789abcde\n"} {
		got, _ := parsePeerBoot(id, "")
		assert.Empty(t, got, "%q", id)
	}
	got, at := parsePeerBoot("0123456789abcdef", "2026-10-21T12:00:00Z")
	assert.Equal(t, "0123456789abcdef", got)
	assert.Equal(t, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), at)
	for _, when := range []string{"", "yesterday", "2026-10-21", "2026-10-21T12:00:00Z" + string(make([]byte, 30))} {
		_, at := parsePeerBoot("0123456789abcdef", when)
		assert.True(t, at.IsZero(), "%q", when)
	}
	assert.Empty(t, (&Session{}).PeerBootID())
	assert.True(t, (&Session{}).PeerStartedAt().IsZero())
}
