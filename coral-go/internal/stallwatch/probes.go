package stallwatch

import (
	"context"
	"log"
	"net/http"
)

// LoggerProbe checks the standard logger, which every log and slog call in Coral goes
// through: log.Writer takes the same lock a write holds, so it waits exactly while a write
// is stuck (a terminal that stopped reading, #3226). It never writes a line.
func LoggerProbe() Probe {
	return Probe{Name: "logger", Check: func(context.Context) error {
		_ = log.Writer()
		return nil
	}}
}

// HTTPProbe sends GET /api/health through the router h in-process, as a loopback caller:
// every middleware runs, so a lock held anywhere on the request path shows up.
func HTTPProbe(h http.Handler) Probe {
	return Probe{Name: "http", Check: func(ctx context.Context) error {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/api/health", nil)
		if err != nil {
			return err
		}
		r.RemoteAddr = "127.0.0.1:1"
		h.ServeHTTP(discardWriter{h: http.Header{}}, r)
		return nil
	}}
}

// DBProbe runs SELECT 1 through ping, which must use the handle's connection pool: with
// SQLite's single connection a stuck query or transaction makes it wait.
func DBProbe(name string, ping func(ctx context.Context) error) Probe {
	return Probe{Name: name, Check: ping}
}

type discardWriter struct{ h http.Header }

func (d discardWriter) Header() http.Header         { return d.h }
func (d discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d discardWriter) WriteHeader(int)             {}
