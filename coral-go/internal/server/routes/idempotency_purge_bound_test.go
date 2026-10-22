package routes

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #5867 review: the purge frees a running record past ITS OWN bound, not past the 8-minute least. A 100-member team launch that is still starting after 8 minutes keeps its record (and
// its key stays "in progress") when another request begins; it is purged only once its own, longer bound has passed. A purge by the constant would free it early and let a retry
// start a second copy of the team, the thing #5859's bound exists to prevent.
func TestPurgeKeepsABigTeamsRunningRecordUntilItsOwnBound(t *testing.T) {
	i := NewIdempotency()
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	i.now = func() time.Time { return clock }
	release, started := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	h := i.Wrap(func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
		w.WriteHeader(200)
	})
	const user = "hub-user-1234abcd"
	body := teamBody(100)
	bound := lifecycleTimeout + 100*lifecyclePerSession + time.Minute
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		idemDo(h, idemReq("/api/sessions/launch-team", body, user, idemKey))
	}()
	<-started
	slots := func() (int, int) { i.mu.Lock(); defer i.mu.Unlock(); return i.perUser[user], len(i.entries) }

	clock = clock.Add(idempotencyRunningMax + time.Second) // past the least bound, within the team's own
	assert.Equal(t, 200, idemDo(h, idemReq("/api/sessions/launch", `{}`, user, "0123456789abcdef-other-1")).Code, "another request begins: the purge runs")
	perUser, entries := slots()
	assert.Equal(t, 2, perUser, "the team's record is still held (and the other request's)")
	assert.Equal(t, 2, entries)
	assert.Equal(t, http.StatusConflict, idemDo(h, idemReq("/api/sessions/launch-team", body, user, idemKey)).Code, "its key is still in progress: no second copy of the team")

	clock = clock.Add(bound - idempotencyRunningMax) // past its own bound
	assert.Equal(t, 200, idemDo(h, idemReq("/api/sessions/launch", `{}`, user, "0123456789abcdef-other-2")).Code)
	perUser, entries = slots()
	assert.Equal(t, 2, entries, "now the team's record is purged: the two other requests' records remain")
	assert.Equal(t, 2, perUser)
	close(release)
	wg.Wait()
}
