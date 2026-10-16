// Package decodelimit bounds the large transcript decodes that run at once in this process
// (#3715, Security's NIT on #3710; #3728 adds the session indexer): a cold chat read, a goals
// read from the start, a tail scan, an indexer pass over a transcript. Each holds or streams a
// transcript and parses it, and several at once (a restart with many live agents) would take
// every CPU from the server; the rest wait their turn.
package decodelimit

import "context"

var slots = make(chan struct{}, 2)

// Big is the size from which a decode takes a slot.
const Big = 1 << 20

// Acquire waits for a slot when size is Big or more, or gives up when ctx ends (a cancelled Hub
// poll doesn't sit behind another session's decode). release gives the slot back; call it on
// every path out (defer).
func Acquire(ctx context.Context, size int64) (release func(), err error) {
	if size < Big {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// InUse is the number of slots taken, for tests.
func InUse() int { return len(slots) }
