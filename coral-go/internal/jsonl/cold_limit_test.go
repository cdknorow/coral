package jsonl

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3715 (Security's review points): at most two large decodes run at once; a third waits, and
// gives up when its context ends; a slot comes back on every path, a panic included.
func TestColdDecodeLimitHoldsAndNeverLeaks(t *testing.T) {
	big := []byte(strings.Repeat(`{"type":"x","v":"`+strings.Repeat("a", 1000)+`"}`+"\n", bigDecode/1000+10))
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
	assert.Len(t, coldDecodes, 2, "both slots taken")

	// A third waits; its context ends first: it gives up at once, takes nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := decodeLimited(ctx, big)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Len(t, coldDecodes, 2)

	close(release)
	wg.Wait()
	assert.Len(t, coldDecodes, 0, "every slot back")

	// A panic during a decode gives its slot back.
	testDuringDecode = func() { panic("boom") }
	func() {
		defer func() { require.NotNil(t, recover()) }()
		_, _ = decodeLimited(context.Background(), big)
	}()
	assert.Len(t, coldDecodes, 0, "the panicking decode's slot is back")

	// A small decode takes no slot at all.
	testDuringDecode = func() { assert.Len(t, coldDecodes, 0) }
	_, err = decodeLimited(context.Background(), []byte(`{"a":1}`+"\n"))
	assert.NoError(t, err)
}
