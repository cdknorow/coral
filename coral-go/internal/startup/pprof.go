//go:build dev

// The profiler is for a dev build only (#6204): a shipped Coral has no profiler, and an environment variable cannot turn one on (pprof_release.go says so in the log).

package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/tmux"
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
	bound, err := startPprofOn(ctx, addr)
	if err != nil {
		log.Printf("[pprof] not started: %v", err)
		return
	}
	enableContentionProfiles()
	log.Printf("[pprof] serving on http://%s/debug/pprof/ (this computer only; block and mutex profiles on)", bound)
	log.Print(pprofWarning)
}

// pprofWarning is logged whenever the profiler is on (#3999, the #3994 audit finding 7): it has no
// credential, so it sits outside strict local auth, and a heap dump holds secrets.
const pprofWarning = "[pprof] WARNING: the profiler needs no password, key or token, so any user of this computer can read heap dumps (which hold the API key and session cookies) through it; turn CORAL_PPROF_ADDR off when you are done"

// Block and mutex profiles are empty unless sampling is turned on; with
// CORAL_PPROF_ADDR set (someone is diagnosing) it is, so /debug/pprof/block and
// /debug/pprof/mutex show where requests wait, e.g. for a database connection
// (#3461). The rates keep the overhead small: one blocking event sampled per 10 us
// spent blocked, one in 100 mutex contentions.
const (
	blockProfileRate     = 10_000 // nanoseconds
	mutexProfileFraction = 100
)

func enableContentionProfiles() {
	runtime.SetBlockProfileRate(blockProfileRate)
	runtime.SetMutexProfileFraction(mutexProfileFraction)
}

// startPprofOn serves the profiling endpoints on addr, which must be a loopback IP
// (port 0 picks one), until ctx ends. It returns the address it listens on.
func startPprofOn(ctx context.Context, addr string) (net.Addr, error) {
	if err := checkLoopbackAddr(addr); err != nil {
		return nil, err
	}
	ln, err := listenTCP(addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// The tmux commands run so far, by name (#4049): execs per minute for tests/perf.
	mux.HandleFunc("/debug/coral/tmux-execs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tmux.ExecCounts())
	})
	// Only Coral's own OS user (#95): the profiler has no credential, so another user of this
	// computer, or a process whose user can't be told, is refused.
	srv := &http.Server{Handler: ownUserOnly(mux), ReadHeaderTimeout: 10 * time.Second, ConnContext: auth.ConnContext}
	go func() {
		defer safego.Recover("startup/pprof:65")
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		defer safego.Recover("startup/pprof:69")
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[pprof] stopped: %v", err)
		}
	}()
	return ln.Addr(), nil
}

// ownUserOnly refuses a request whose loopback connection isn't Coral's own OS user's (auth.PeerReason).
func ownUserOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := auth.PeerReason(r); reason != "" {
			http.Error(w, "the profiler is only for the account Coral runs as ("+reason+")", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
