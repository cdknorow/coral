package logdedupe

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #4112: the first line with a key is logged, repeats within the window are counted, and the next
// one after it is logged with the count; other keys are their own.
func TestRepeatsAreCountedNotLogged(t *testing.T) {
	l := New(5 * time.Minute)
	at := time.Unix(0, 0)
	l.now = func() time.Time { return at }

	ok, n := l.Allow("send failed: pane a")
	assert.True(t, ok)
	assert.Zero(t, n)
	for i := 0; i < 99; i++ {
		at = at.Add(time.Second)
		ok, _ = l.Allow("send failed: pane a")
		assert.False(t, ok)
	}
	ok, _ = l.Allow("send failed: pane b")
	assert.True(t, ok, "another key is logged")

	at = at.Add(5 * time.Minute)
	ok, n = l.Allow("send failed: pane a")
	assert.True(t, ok, "after the window")
	assert.Equal(t, 99, n, "with how many were held back")
	at = at.Add(time.Second)
	ok, _ = l.Allow("send failed: pane a")
	assert.False(t, ok, "and the window starts again")
}

func TestKeysAreBounded(t *testing.T) {
	l := New(time.Hour)
	for i := 0; i < keysMax+10; i++ {
		l.Allow(strconv.Itoa(i))
	}
	assert.LessOrEqual(t, len(l.m), keysMax)
}

func TestConcurrentUse(t *testing.T) {
	l := New(time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	logged := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("k"); ok {
				mu.Lock()
				logged++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, logged)
}
