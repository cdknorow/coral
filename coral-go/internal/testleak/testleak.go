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

	"go.uber.org/goleak"
)

// Ignore are goroutines that live for the whole process by design, by their top function.
var Ignore = []goleak.Option{
	// gopsutil's load average on Windows: the first load.Avg starts one sampler for the process
	// (a sync.Once), and it can still be setting up its performance counter, inside a syscall,
	// when the server context it was given has already ended.
	goleak.IgnoreAnyFunction("github.com/shirou/gopsutil/v4/load.loadAvgGoroutine"),
}

// Check returns code, or 1 when the tests passed but left goroutines running (they are printed).
func Check(code int, extra ...goleak.Option) int {
	if code != 0 {
		return code
	}
	// Keep-alive connections idle in the default client's pool are the pool, not a leak: closed
	// first, so what is left is what the tests started and never stopped.
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	if err := goleak.Find(append(append([]goleak.Option{}, Ignore...), extra...)...); err != nil {
		fmt.Fprintf(os.Stderr, "testleak: goroutines still running after the tests (#4167):\n%v\n", err)
		return 1
	}
	return code
}
