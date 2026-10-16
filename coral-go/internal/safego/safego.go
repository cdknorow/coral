// Package safego keeps one goroutine's panic from ending the whole server (#3998, Security's
// #3994 finding 2). A panic in any goroutine ends the process, and with the pty backend every
// agent runs inside it, so every goroutine the server starts recovers: a panic is logged once,
// with its stack, through the server's (redacting) log, and the server keeps running.
//
// Use it as the first line of the goroutine, so the go statement keeps its meaning:
//
//	go func() {
//		defer safego.Recover("tunnel stream")
//		...
//	}()
//
// A long-running service that must keep going uses Loop, which restarts it after a panic.
// TestEveryGoroutineRecovers (in this package) fails on a go statement without Recover.
package safego

import (
	"context"
	"log"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// panics counts recovered panics (tests and the stall report).
var panics atomic.Int64

// Panics is how many goroutine panics were recovered since start.
func Panics() int64 { return panics.Load() }

// Recover, deferred as the goroutine's first statement, recovers a panic and logs it with its
// stack. It must be deferred directly (defer safego.Recover(name)), or recover() sees nothing.
func Recover(name string) {
	if r := recover(); r != nil {
		panics.Add(1)
		log.Printf("[CRASH] goroutine %q panicked (the server keeps running): %v\n%s", name, r, debug.Stack())
	}
}

// Go runs fn in a new goroutine that recovers a panic.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

// loopBackoff is the first restart delay; it doubles up to loopBackoffMax after each failure in
// a row, and starts again after a run that lasted loopHealthy.
var (
	loopBackoff    = 5 * time.Second
	loopBackoffMax = 2 * time.Minute
	loopHealthy    = 5 * time.Minute
)

// Loop runs fn, a service that should run until ctx ends, in a new goroutine. A panic or an
// unexpected return restarts it after a backoff (5 s, doubling to 2 minutes; back to 5 s after a
// run of 5 minutes). It stops when ctx ends.
func Loop(ctx context.Context, name string, fn func()) {
	go func() {
		defer Recover(name)
		delay := loopBackoff
		for {
			start := time.Now()
			panicked := true
			func() {
				defer Recover(name)
				fn()
				panicked = false
			}()
			if ctx.Err() != nil {
				return
			}
			if !panicked {
				log.Printf("[WARN] background service %q returned unexpectedly", name)
			}
			if time.Since(start) >= loopHealthy {
				delay = loopBackoff
			}
			log.Printf("[RESTART] background service %q restarting in %s", name, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay *= 2; delay > loopBackoffMax {
				delay = loopBackoffMax
			}
		}
	}()
}

// Restart runs fn in a new goroutine and runs it again, after a backoff, each time it panics; it
// stops when fn returns normally. For a loop that ends on its own (a listener closing, ctx done)
// but must not be lost to a panic.
func Restart(name string, fn func()) {
	go func() {
		defer Recover(name)
		delay := loopBackoff
		for {
			panicked := true
			func() {
				defer Recover(name)
				fn()
				panicked = false
			}()
			if !panicked {
				return
			}
			log.Printf("[RESTART] %q restarting in %s after a panic", name, delay)
			time.Sleep(delay)
			if delay *= 2; delay > loopBackoffMax {
				delay = loopBackoffMax
			}
		}
	}()
}
