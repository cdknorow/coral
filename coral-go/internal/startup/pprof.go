package startup

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// startPprof serves Go profiling endpoints when CORAL_PPROF_ADDR is set, e.g.
// CORAL_PPROF_ADDR=127.0.0.1:6060. It is off by default, listens on its own
// port (never the dashboard router), and only on a loopback address: profiles
// expose memory contents and goroutine stacks.
func startPprof(ctx context.Context) {
	addr := os.Getenv("CORAL_PPROF_ADDR")
	if addr == "" {
		return
	}
	if err := checkLoopbackAddr(addr); err != nil {
		log.Printf("[pprof] not started: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		log.Printf("[pprof] serving on http://%s/debug/pprof/", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[pprof] stopped: %v", err)
		}
	}()
}

// checkLoopbackAddr accepts host:port only when host is a loopback IP literal.
// Hostnames are refused, since they could resolve to a routable address.
func checkLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("CORAL_PPROF_ADDR %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("CORAL_PPROF_ADDR %q must use a loopback IP such as 127.0.0.1 or [::1]", addr)
	}
	return nil
}
