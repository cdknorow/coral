package server

import (
	"log"
	"net/http"
	"runtime/debug"
)

// recoverer answers 500 to a request whose handler panicked, and logs the panic through the log
// package (#3763, secrets audit #3762 finding 4): chi's Recoverer prints the panic value straight
// to stderr, which skips the redaction every coral.log line goes through, and a panic value can
// hold request data. A panic with http.ErrAbortHandler is passed on, as net/http expects.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}
			if rvr == http.ErrAbortHandler {
				panic(rvr)
			}
			log.Printf("[panic] %s %s: %v\n%s", r.Method, r.URL.Path, rvr, debug.Stack())
			if r.Header.Get("Connection") != "Upgrade" {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
