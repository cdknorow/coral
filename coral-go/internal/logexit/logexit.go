// Package logexit lets code anywhere in the server exit after a fatal log line
// without losing that line from the terminal. The coral binary copies log lines
// to the terminal from a queue (#3226), so exiting straight after logging would
// drop the last lines, which are usually the ones that say why it stopped.
package logexit

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// flushTimeout bounds how long an exit waits for the terminal: a terminal that
// is not reading must not keep the process from exiting.
const flushTimeout = 2 * time.Second

var (
	mu    sync.Mutex
	flush func(time.Duration)
)

// SetFlush registers the function that writes queued log lines out, waiting at
// most the given time. Nil removes it.
func SetFlush(f func(time.Duration)) {
	mu.Lock()
	defer mu.Unlock()
	flush = f
}

// Flush writes queued log lines out, waiting at most flushTimeout.
func Flush() {
	mu.Lock()
	f := flush
	mu.Unlock()
	if f != nil {
		f(flushTimeout)
	}
}

// Fatalf is log.Fatalf that flushes the log before exiting.
func Fatalf(format string, v ...any) {
	log.Output(2, fmt.Sprintf(format, v...))
	Exit(1)
}

// Exit flushes the log and exits with code.
func Exit(code int) {
	Flush()
	os.Exit(code)
}
