package routes

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowScan replaces the live scan's rules with one that takes hold to finish and records how many scans ran at
// once; it returns the highest count.
func slowScan(t *testing.T, hold time.Duration) *atomic.Int32 {
	t.Helper()
	var active, most atomic.Int32
	prev := liveScan.text
	liveScan.text = func(_ time.Duration, v []byte) ([]byte, error) {
		n := active.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(hold)
		active.Add(-1)
		return v, nil
	}
	t.Cleanup(func() { liveScan.text = prev })
	return &most
}

func textAttachment() []byte { return []byte(strings.Repeat("plain words of a report\n", 40)) }

// #4474 (Security's #4464 INFO 1): two sends at once are scanned one after the other, never together, and the
// slot is free again afterwards.
func TestAttachmentScansRunOneAtATime(t *testing.T) {
	most := slowScan(t, 30*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, textAttachment()))
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), most.Load(), "never two scans at once")
	assert.Empty(t, scanSlot, "the slot is free again")
}

// A waiter whose request ends gives up its place without taking the slot, and the next send scans at once.
func TestACancelledScanWaiterFreesItsPlace(t *testing.T) {
	release, err := acquireScanSlot(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := acquireScanSlot(ctx); done <- err }()
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	release()
	assert.Empty(t, scanSlot, "the cancelled waiter took nothing")
	release, err = acquireScanSlot(context.Background())
	require.NoError(t, err, "the next send gets the slot")
	release()
}

// Past scanQueueWait (a fake clock) a send is refused with attach_busy and none of its file is scanned.
func TestAttachBusyPastTheQueueWait(t *testing.T) {
	release, err := acquireScanSlot(context.Background())
	require.NoError(t, err)
	defer release()
	waited := make(chan time.Duration, 1)
	prev := scanQueueAfter
	scanQueueAfter = func(d time.Duration) <-chan time.Time {
		waited <- d
		c := make(chan time.Time, 1)
		c <- time.Now() // the wait has run out
		return c
	}
	t.Cleanup(func() { scanQueueAfter = prev })
	scanned := slowScan(t, 0)
	r := scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, textAttachment())
	require.NotNil(t, r)
	assert.Equal(t, http.StatusServiceUnavailable, r.status)
	assert.Equal(t, "attach_busy", r.code)
	assert.Equal(t, scanQueueWait, <-waited)
	assert.Zero(t, scanned.Load(), "nothing was scanned")
}

// The slot is given back whatever the scan found: a secret, or a file it could not check in time.
func TestTheScanSlotIsReleasedAfterEveryOutcome(t *testing.T) {
	r := scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, append(textAttachment(), []byte("token=ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R\n")...))
	require.NotNil(t, r)
	assert.Equal(t, "attach_secret", r.code)
	assert.Empty(t, scanSlot)
	prev := liveScan.text
	liveScan.text = func(time.Duration, []byte) ([]byte, error) { return nil, context.DeadlineExceeded }
	t.Cleanup(func() { liveScan.text = prev })
	r = scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, textAttachment())
	require.NotNil(t, r)
	assert.Equal(t, "attach_unchecked", r.code)
	assert.Empty(t, scanSlot)
	// A binary file is not scanned and never waits for the slot.
	release, err := acquireScanSlot(context.Background())
	require.NoError(t, err)
	defer release()
	assert.Nil(t, scanAttachment(context.Background(), attachedArtifact{ID: "ab"}, bytes.Repeat([]byte{0, 1, 2, 0xff}, 64)))
}
