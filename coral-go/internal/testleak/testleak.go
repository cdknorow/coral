// Package testleak is the goroutine leak check the test mains of the packages that start
// background work run after their tests (#4167): a goroutine still running when every test is
// done is a missing stop, a context never cancelled or a ticker never stopped, unless it is one of
// the process-wide workers listed in Ignore (each with the reason it lives for the whole process).
// Only test files import it, so goleak never reaches a Coral binary.
package testleak

import (
	"fmt"
	"net/http"
	"os"
	"runtime"

	"go.uber.org/goleak"
)

// Ignore are goroutines that live for the whole process by design, by their top function.
var Ignore = []goleak.Option{
	// gopsutil's load average on Windows: the first load.Avg starts one sampler for the process
	// (a sync.Once), and it can still be setting up its performance counter, inside a syscall,
	// when the server context it was given has already ended.
	goleak.IgnoreAnyFunction("github.com/shirou/gopsutil/v4/load.loadAvgGoroutine"),
	// The guardian client's watcher (internal/ptymanager, #6129): one goroutine for the process waits on the guardian that ends terminals when Coral dies, and starts another if it exits.
	goleak.IgnoreAnyFunction("github.com/subgentic/coral/internal/ptymanager.(*guardianClient).watch"),
}

// Check returns code, or 1 when the tests passed but left goroutines running (they are printed). It looks for leaks even when
// some test failed (code != 0), and prints what it finds, so a package whose tests always fail somewhere (as on a machine with
// known failures) is not hidden from the check; the failure code is returned as it was.
func Check(code int, extra ...goleak.Option) int {
	// Keep-alive connections idle in the default client's pool are the pool, not a leak: closed
	// first, so what is left is what the tests started and never stopped.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	if err := goleak.Find(append(append([]goleak.Option{}, Ignore...), extra...)...); err != nil {
		fmt.Fprintf(os.Stderr, "testleak: goroutines still running after the tests (#4167):\n%v\n", err)
		// A leaked goroutine's creator may be gone (a database/sql Rows.awaitDone names none), so what else is running
		// at this moment is the only way to find who held it (#146).
		buf := make([]byte, 1<<20)
		fmt.Fprintf(os.Stderr, "testleak: every goroutine at this moment:\n%s\n", buf[:runtime.Stack(buf, true)])
		if code == 0 {
			return 1
		}
		fmt.Fprintf(os.Stderr, "testleak: (the tests had failed already, so the exit code stays %d; a failing test can leave goroutines of its own)\n", code)
	}
	return code
}
