package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeOptimizer struct {
	mu     sync.Mutex
	passes []bool
	err    error
}

func (f *fakeOptimizer) Optimize(_ context.Context, first bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passes = append(f.passes, first)
	return f.err
}

func (f *fakeOptimizer) got() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.passes...)
}

// #4142: the first pass is the fuller one, later ones the plain form; one database failing
// doesn't stop the other.
func TestDBOptimizeFirstThenPlain(t *testing.T) {
	a, b := &fakeOptimizer{err: errors.New("busy")}, &fakeOptimizer{}
	j := NewDBOptimize(map[string]Optimizer{"a": a, "b": b})
	j.First, j.Interval = time.Millisecond, time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	assert.Eventually(t, func() bool { return len(b.got()) >= 3 }, 5*time.Second, time.Millisecond)
	cancel()
	<-done
	for _, f := range []*fakeOptimizer{a, b} {
		p := f.got()
		assert.True(t, p[0], "the first pass")
		for _, first := range p[1:] {
			assert.False(t, first, "later passes")
		}
	}
}
