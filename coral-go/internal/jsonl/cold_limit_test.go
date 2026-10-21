package jsonl

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/decodelimit"
)

// #3715 (Security's review points): at most two large decodes run at once; a third waits, and
// gives up when its context ends; a slot comes back on every path, a panic included.
func TestColdDecodeLimitHoldsAndNeverLeaks(t *testing.T) {
	big := []byte(strings.Repeat(`{"type":"x","v":"`+strings.Repeat("a", 1000)+`"}`+"\n", decodelimit.Big/1000+10))
	inside, release := make(chan struct{}, 4), make(chan struct{})
	testDuringDecode = func() { inside <- struct{}{}; <-release }
	t.Cleanup(func() { testDuringDecode = nil })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = decodeLimited(context.Background(), big) }()
	}
	<-inside
	<-inside
	assert.Equal(t, 2, decodelimit.InUse(), "both slots taken")

	// A third waits; its context ends first: it gives up at once, takes nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := decodeLimited(ctx, big)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 2, decodelimit.InUse())

	close(release)
	wg.Wait()
	assert.Equal(t, 0, decodelimit.InUse(), "every slot back")

	// A panic during a decode gives its slot back.
	testDuringDecode = func() { panic("boom") }
	func() {
		defer func() { require.NotNil(t, recover()) }()
		_, _ = decodeLimited(context.Background(), big)
	}()
	assert.Equal(t, 0, decodelimit.InUse(), "the panicking decode's slot is back")

	// A small decode takes no slot at all.
	testDuringDecode = func() { assert.Equal(t, 0, decodelimit.InUse()) }
	_, err = decodeLimited(context.Background(), []byte(`{"a":1}`+"\n"))
	assert.NoError(t, err)
}
