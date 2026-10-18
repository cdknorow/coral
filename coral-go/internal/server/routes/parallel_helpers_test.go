package routes

import (
	"runtime"
	"sync"
)

// #4852: the old-against-new oracles are the slowest tests of this package (a hundred seconds of the whole run). Their units of work (a seed, a
// block of random commands) share nothing, so they run on several goroutines: the same work, the same checks, a fraction of the time. A unit
// reports a failure with t.Error (never t.Fatal: that is for the test's own goroutine) and returns false to stop its worker.

// testWorkers is how many goroutines an oracle uses: the CPUs, at most 8 (a bigger fan-out only adds contention on the SQLite files).
func testWorkers() int {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// parallelFor runs fn(i) for i in [0, n) on testWorkers goroutines and waits. fn returns false to stop the calling worker.
func parallelFor(n int, fn func(i int) bool) {
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < testWorkers(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if !fn(i) {
					// drain so the feeder never blocks on a stopped worker's share
					for range next {
					}
					return
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}
